package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"localbackup/internal/backup"
	"localbackup/internal/store"
)

type previewJob struct {
	PreviewID   string          `json:"previewId,omitempty"`
	ID          string          `json:"id"`
	PlanID      string          `json:"planId"`
	PlanName    string          `json:"planName"`
	SourceIDs   []string        `json:"sourceIds,omitempty"`
	Fingerprint string          `json:"fingerprint"`
	State       string          `json:"state"`
	Stage       string          `json:"stage"`
	CreatedAt   time.Time       `json:"createdAt"`
	FinishedAt  *time.Time      `json:"finishedAt,omitempty"`
	PathsRead   int64           `json:"pathsRead"`
	BytesRead   int64           `json:"bytesRead"`
	CurrentPath string          `json:"currentPath,omitempty"`
	Error       string          `json:"error,omitempty"`
	Result      *backup.Preview `json:"result,omitempty"`
}
type livePreview struct {
	job    previewJob
	cancel context.CancelFunc
	done   chan struct{}
}
type previewState struct {
	enqueueMu sync.Mutex
	mu        sync.Mutex
	live      map[string]*livePreview
	slot      chan struct{}
}

func (s *Server) recoverPreviewJobs() {
	s.previews.live = map[string]*livePreview{}
	s.previews.slot = make(chan struct{}, 1)
	jobs, _ := store.List[previewJob](s.Store, "preview_jobs")
	for _, job := range jobs {
		if job.FinishedAt == nil {
			now := time.Now().UTC()
			job.State, job.Stage, job.Error, job.FinishedAt = "failed", "扫描已中断", "服务已重启，可重新扫描；没有开始备份或删除文件。", &now
			s.Store.Put("preview_jobs", job.ID, job)
		}
	}
}
func (s *Server) enqueuePreview(p backup.Plan, ids []string) (previewJob, error) {
	_, ids, err := scopeSources(p, ids)
	if err != nil {
		return previewJob{}, err
	}
	s.previews.enqueueMu.Lock()
	defer s.previews.enqueueMu.Unlock()
	s.previews.mu.Lock()
	fp := fingerprint(p)
	for _, live := range s.previews.live {
		if live.job.PlanID == p.ID && live.job.Fingerprint == fp && scopeJSON(ids) == scopeJSON(live.job.SourceIDs) && live.job.State != "cancelling" {
			job := live.job
			s.previews.mu.Unlock()
			return job, nil
		}
	}
	if len(s.previews.live) >= 20 {
		s.previews.mu.Unlock()
		return previewJob{}, fmt.Errorf("后台扫描队列已满，请先取消不需要的扫描")
	}
	s.previews.mu.Unlock()
	job := previewJob{ID: backup.ID("scan-"), PlanID: p.ID, PlanName: p.Name, SourceIDs: ids, Fingerprint: fp, State: "queued", Stage: "等待后台扫描", CreatedAt: time.Now().UTC()}
	if err = s.Store.Put("preview_jobs", job.ID, job); err != nil {
		return job, err
	}
	ctx, cancel := context.WithCancel(s.ctx)
	live := &livePreview{job: job, cancel: cancel, done: make(chan struct{})}
	s.previews.mu.Lock()
	s.previews.live[job.ID] = live
	s.previews.mu.Unlock()
	go s.workPreview(ctx, p, live)
	return job, nil
}
func (s *Server) workPreview(ctx context.Context, p backup.Plan, live *livePreview) {
	defer close(live.done)
	defer live.cancel()
	var result backup.Preview
	var err error
	select {
	case s.previews.slot <- struct{}{}:
		defer func() { <-s.previews.slot }()
		s.previews.mu.Lock()
		live.job.State, live.job.Stage = "running", "正在扫描目录并校验文件内容"
		s.previews.mu.Unlock()
		watchdog := time.AfterFunc(10*time.Second, func() {
			buf := make([]byte, 1<<20)
			n := runtime.Stack(buf, true)
			_ = os.WriteFile(filepath.Join(filepath.Dir(s.Store.Path), "preview-stacks.log"), buf[:n], 0600)
		})
		defer watchdog.Stop()
		observed := backup.Observe(ctx, func(event backup.WorkEvent) {
			s.previews.mu.Lock()
			defer s.previews.mu.Unlock()
			if strings.HasPrefix(event.Phase, "preview:") {
				live.job.Stage = strings.TrimPrefix(event.Phase, "preview:")
			}
			live.job.BytesRead += event.Bytes
			if event.Path != "" {
				live.job.PathsRead++
				live.job.CurrentPath = event.Path
			}
		})
		var current backup.Plan
		if e := s.Store.Get("backup_plans", p.ID, &current); e != nil || fingerprint(current) != live.job.Fingerprint {
			err = fmt.Errorf("计划已变更，请按最新配置重新扫描")
		} else {
			result, err = s.createPreview(observed, p, live.job.SourceIDs)
			if err == nil {
				if e := s.Store.Get("backup_plans", p.ID, &current); e != nil || fingerprint(current) != live.job.Fingerprint {
					err = fmt.Errorf("扫描期间计划已变更，请重新扫描")
				}
			}
		}
	case <-ctx.Done():
		err = ctx.Err()
	}
	s.previews.mu.Lock()
	now := time.Now().UTC()
	live.job.FinishedAt = &now
	if ctx.Err() != nil {
		live.job.State, live.job.Stage = "cancelled", "扫描已取消"
	} else if err != nil {
		live.job.State, live.job.Stage, live.job.Error = "failed", "扫描未完成", err.Error()
	} else {
		live.job.State, live.job.Stage, live.job.Result = "ready", "扫描完成，等待预览确认", &result
	}
	saved := live.job
	s.previews.mu.Unlock()
	persistErr := s.Store.Put("preview_jobs", saved.ID, saved)
	s.previews.mu.Lock()
	defer s.previews.mu.Unlock()
	if e := persistErr; e != nil {
		// Keep the terminal result available in memory instead of claiming the
		// interrupted durable record was successfully saved.
		live.job.State, live.job.Error = "failed", "无法保存扫描结果："+e.Error()
		live.job.Result = nil
		return
	}
	delete(s.previews.live, live.job.ID)
}
func (s *Server) previewJobs(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) == 1 && r.Method == "GET" {
		// Results can contain tens of thousands of operations. Only return them
		// when a specific result is opened, never in the periodic status poll.
		rows, err := s.Store.DB.Query("SELECT json_set(json_remove(data,'$.result'),'$.previewId',json_extract(data,'$.result.previewId')) FROM preview_jobs ORDER BY rowid DESC LIMIT 30")
		if err != nil {
			fail(w, 500, err.Error())
			return
		}
		defer rows.Close()
		jobs := []previewJob{}
		for rows.Next() {
			var raw string
			var job previewJob
			if err = rows.Scan(&raw); err != nil {
				fail(w, 500, err.Error())
				return
			}
			if err = json.Unmarshal([]byte(raw), &job); err != nil {
				fail(w, 500, err.Error())
				return
			}
			s.previews.mu.Lock()
			if live := s.previews.live[job.ID]; live != nil {
				job = live.job
			}
			s.previews.mu.Unlock()
			if job.Result != nil {
				job.PreviewID = job.Result.PreviewID
			}
			job.Result = nil
			jobs = append(jobs, job)
		}
		if err := rows.Err(); err != nil {
			fail(w, 500, err.Error())
			return
		}
		sort.SliceStable(jobs, func(i, j int) bool { return jobs[i].CreatedAt.After(jobs[j].CreatedAt) })
		reply(w, 200, jobs)
		return
	}
	if len(parts) < 2 {
		fail(w, 404, "扫描不存在")
		return
	}
	var job previewJob
	if err := s.Store.Get("preview_jobs", parts[1], &job); err != nil {
		fail(w, 404, "扫描不存在")
		return
	}
	s.previews.mu.Lock()
	if live := s.previews.live[job.ID]; live != nil {
		if live.job.FinishedAt == nil && len(parts) == 3 && parts[2] == "cancel" && r.Method == "POST" {
			live.cancel()
			live.job.State, live.job.Stage = "cancelling", "正在取消扫描"
		}
		job = live.job
	}
	s.previews.mu.Unlock()
	if len(parts) == 2 && r.Method == "GET" || len(parts) == 3 && parts[2] == "cancel" && r.Method == "POST" {
		reply(w, 200, job)
		return
	}
	fail(w, 404, "接口不存在")
}
