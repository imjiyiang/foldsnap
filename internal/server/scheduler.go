package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"localbackup/internal/backup"
)

type checkpoint struct {
	FirstObservedAt     time.Time       `json:"firstObservedAt"`
	LastEvaluatedAt     time.Time       `json:"lastEvaluatedAt"`
	LastChangeCheck     time.Time       `json:"lastChangeCheck"`
	LastMaxCheck        time.Time       `json:"lastMaxCheck"`
	ChangeRevision      int64           `json:"changeRevision"`
	ChangeSignature     string          `json:"changeSignature"`
	ChangeRunID         string          `json:"changeRunId"`
	Changes             *ChangeSummary  `json:"changes,omitempty"`
	Decision            string          `json:"decision,omitempty"`
	MaxBackupPending    bool            `json:"maxBackupPending"`
	RetryPending        bool            `json:"retryPending"`
	RetryTrigger        string          `json:"retryTrigger,omitempty"`
	RetryDestinationIDs []string        `json:"retryDestinationIds,omitempty"`
	TargetOnline        map[string]bool `json:"targetOnline,omitempty"`
	PendingMounts       map[string]bool `json:"pendingMounts,omitempty"`
	LastVolumeCheck     time.Time       `json:"lastVolumeCheck"`
	VolumeEpoch         uint64          `json:"volumeEpoch"`
	LastAudit           time.Time       `json:"lastAudit"`
	Signature           string          `json:"signature"`
	ChangedAt           time.Time       `json:"changedAt"`
	Online              bool            `json:"online"`
	LastAttempt         time.Time       `json:"lastAttempt"`
	Failures            int             `json:"failures"`
	LastRunID           string          `json:"lastRunId"`
}

func signature(ctx context.Context, p backup.Plan) (string, error) {
	var sourceErr error
	p, sourceErr = backup.ResolveSources(p)
	if sourceErr != nil {
		return "", sourceErr
	}
	p.EvaluatedAt = backup.EvaluationTime(p)
	h := sha256.New()
	for _, s := range p.Sources {
		if !s.Enabled {
			continue
		}
		selector, e := backup.CompileSelection(p, s)
		if e != nil {
			return "", e
		}
		e = filepath.WalkDir(s.Path, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if err = ctx.Err(); err != nil {
				return err
			}
			for _, excluded := range p.RuntimeExcludes {
				if backup.Contains(excluded, path) {
					if d.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
			}
			rel, _ := filepath.Rel(s.Path, path)
			st, err := d.Info()
			if err != nil {
				return err
			}
			if path != s.Path {
				kind := "file"
				if st.IsDir() {
					kind = "directory"
				} else if st.Mode()&os.ModeSymlink != 0 {
					kind = "symlink"
				}
				include, _ := selector.Match(filepath.ToSlash(rel), kind, st.Size(), st.ModTime())
				if !include {
					if d.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
			}
			if path == s.Path {
				return nil
			}
			if d.IsDir() {
				fmt.Fprintf(h, "%s/%q:%d\n", s.ID, path, st.Mode())
			} else {
				fmt.Fprintf(h, "%s/%q:%d:%d:%d:%s\n", s.ID, path, st.Size(), st.ModTime().UnixNano(), st.Mode(), backup.MetadataVersion(st))
			}
			return nil
		})
		if e != nil {
			return "", e
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func (s *Server) Start(ctx context.Context) {
	s.ctx = ctx
	s.startMonitor(ctx)
	go s.refreshRsync()
	// Repository read locks keep recovery safe alongside normal job validation.
	// A sleeping/offline disk must not hold up unrelated queued backups.
	go s.reconcile(ctx)
	go s.queueLoop(ctx)
	go s.watchLoop(ctx)
	go func() {
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				s.mu.Lock()
				if s.cancel != nil {
					s.cancel()
				}
				s.mu.Unlock()
				return
			case <-s.wake:
				s.tick(ctx)
			case <-timer.C:
				s.tick(ctx)
				seconds := s.config().PollSeconds
				if seconds < 10 {
					seconds = 30
				}
				if s.nativeHealthy() {
					seconds = 2
				}
				timer.Reset(time.Duration(seconds) * time.Second)
			}
		}
	}()
}
func (s *Server) tick(ctx context.Context) {
	plans, e := s.Store.Plans()
	if e != nil {
		return
	}
	runs, _ := s.Store.Runs()
	for _, p := range plans {
		pendingPlan := false
		for _, run := range runs {
			if run.PlanID == p.ID && run.FinishedAt == nil {
				pendingPlan = true
				break
			}
		}
		p = s.scanPlan(p)
		if !p.Enabled {
			continue
		}
		if !p.Schedule.OnChange && !p.Schedule.OnMount && p.Schedule.IntervalMinutes == 0 && p.Schedule.MaxIntervalMinutes == 0 {
			continue
		}
		var cp checkpoint
		s.Store.Get("scan_checkpoints", p.ID, &cp)
		now := time.Now()
		cp.LastEvaluatedAt = now
		if cp.FirstObservedAt.IsZero() {
			cp.FirstObservedAt = now
		}

		s.watchMu.RLock()
		epoch := s.mountEpoch
		s.watchMu.RUnlock()
		period := s.config().PollSeconds
		if period < 10 {
			period = 30
		}
		if !s.nativeHealthy() || cp.LastVolumeCheck.IsZero() || cp.VolumeEpoch != epoch || time.Since(cp.LastVolumeCheck) >= time.Duration(period)*time.Second {
			// A new service session reconciles all connected disks once. Subsequent
			// events compare each target independently, even while another run waits.
			if cp.LastVolumeCheck.Before(s.startedAt) {
				cp.TargetOnline = nil
			}
			observeMounts(&cp, p, backup.CheckDestination)
			cp.LastVolumeCheck = time.Now()
			cp.VolumeEpoch = epoch
		}
		mountIDs := pendingMountIDs(cp, p)
		if pendingPlan {
			cp.Decision = "已有未完成任务，等待队列"
			s.Store.Put("scan_checkpoints", p.ID, cp)
			continue
		}
		var last *backup.Run
		var successAt time.Time
		successRunID := ""
		for _, r := range runs {
			if r.PlanID != p.ID || r.FinishedAt == nil {
				continue
			}
			if successRunID == "" && r.Status == "success" {
				successRunID = r.ID
			}
			if last == nil && len(r.SourceIDs) == 0 {
				copy := r
				last = &copy
			}
			if successAt.IsZero() && r.Status == "success" && len(r.SourceIDs) == 0 && len(r.DestinationIDs) == 0 {
				successAt = *r.FinishedAt
			}
		}
		if last != nil && last.ID != cp.LastRunID {
			cp.LastRunID = last.ID
			switch {
			case last.Status == "success":
				cp.Failures = 0
				cp.RetryPending = false
				cp.RetryDestinationIDs = nil
				cp.LastAttempt = time.Time{}
			case last.Status == "failed" && last.Trigger != "manual":
				cp.Failures++
				cp.LastAttempt = *last.FinishedAt
				cp.RetryPending = cp.Failures < p.Schedule.AutomaticAttempts()
				cp.RetryTrigger = last.Trigger
				cp.RetryDestinationIDs = append([]string(nil), last.DestinationIDs...)
			case last.Status == "cancelled":
				cp.RetryPending = false
				cp.LastAttempt = *last.FinishedAt
			default:
				cp.RetryPending = false
			}
		}
		if last != nil && last.Status == "needs_confirmation" {
			cp.Decision = "任务等待重新预览并确认"
			s.Store.Put("scan_checkpoints", p.ID, cp)
			continue
		}
		retryOnline := cp.Online
		if len(cp.RetryDestinationIDs) > 0 {
			retryOnline = true
			for _, id := range cp.RetryDestinationIDs {
				if !cp.TargetOnline[id] {
					retryOnline = false
				}
			}
		}
		if !cp.Online && !(p.Schedule.OnMount && len(mountIDs) > 0) && !(cp.RetryPending && retryOnline) {
			cp.Decision = "等待已绑定的目标磁盘"
			s.Store.Put("scan_checkpoints", p.ID, cp)
			continue
		}
		mountReady := p.Schedule.OnMount && len(mountIDs) > 0
		if cp.Failures >= p.Schedule.AutomaticAttempts() && !mountReady {
			cp.Decision = fmt.Sprintf("连续失败 %d 次，自动尝试已停止", cp.Failures)
			s.Store.Put("scan_checkpoints", p.ID, cp)
			continue
		}
		if (cp.Failures > 0 || (last != nil && last.Status == "cancelled")) && !cp.LastAttempt.IsZero() && now.Before(cp.LastAttempt.Add(retryDelay(cp.Failures))) && !mountReady {
			cp.Decision = "失败退避中，等待下一次自动尝试"
			s.Store.Put("scan_checkpoints", p.ID, cp)
			continue
		}
		trigger := ""
		request := RunRequest{AutomaticAttempt: 1}
		switch {
		case mountReady:
			trigger = "volume"
			request.DestinationIDs = mountIDs
			cp.Failures = 0
			cp.RetryPending = false
			cp.RetryDestinationIDs = nil
		case cp.RetryPending && retryOnline:
			trigger = cp.RetryTrigger
			request.DestinationIDs = cp.RetryDestinationIDs
			request.AutomaticAttempt = cp.Failures + 1
			request.RetryOf = cp.LastRunID
		case cp.Online && p.Schedule.IntervalMinutes > 0 && (successAt.IsZero() || now.Sub(successAt) >= time.Duration(p.Schedule.IntervalMinutes)*time.Minute):
			trigger = "schedule"
		}
		if trigger == "" && cp.Online && (p.Schedule.OnChange || p.Schedule.MaxIntervalMinutes > 0 || cp.MaxBackupPending) {
			maxDue := maximumCheckDue(p, cp, successAt, now)
			ready, err := s.evaluateChanges(ctx, p, &cp, successAt, successRunID, maxDue)
			if err != nil {
				cp.Decision = "检查未完成：" + err.Error()
				s.Store.Put("scan_checkpoints", p.ID, cp)
				continue
			}
			if cp.MaxBackupPending {
				trigger = "max_interval"
			} else if ready {
				trigger = "change"
			}
		}
		if trigger != "" {
			run, err := s.startWithRequest(p, trigger, request)
			if err == nil {
				cp.LastAttempt = now
				cp.ChangedAt = time.Time{}
				cp.RetryPending = false
				cp.MaxBackupPending = false
				cp.Decision = "已入队 · " + trigger
				if run.Trigger == "volume" {
					for _, id := range run.DestinationIDs {
						delete(cp.PendingMounts, id)
					}
				}
				s.Store.Put("scan_checkpoints", p.ID, cp)
				return
			}
			cp.Decision = "入队未完成：" + err.Error()
		}

		s.Store.Put("scan_checkpoints", p.ID, cp)
	}
}

// Full hash comparison is required after a journal gap: file metadata alone is
// insufficient evidence that a source remained unchanged while events were lost.
func (s *Server) contentChanged(ctx context.Context, p backup.Plan, full ...bool) (bool, error) {
	force := true
	if len(full) > 0 {
		force = full[0]
	}
	summary, err := s.changeSummary(ctx, p, force)
	return summary.Changed, err
}
