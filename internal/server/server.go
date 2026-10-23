package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"localbackup/internal/backup"
	"localbackup/internal/store"
	"localbackup/internal/webui"
)

var errPlanBusy = errors.New("任务运行期间不能修改计划")

type Server struct {
	preflightCache preflightCache
	previews       previewState
	recoveries     map[string]recoveryGrant
	startedAt      time.Time
	configMu       sync.Mutex
	runSaveMu      sync.Mutex
	control        *backup.Control
	monitor        monitorState
	Store          *store.Store
	Token          string
	Host           string
	Dev            bool
	Web            string
	mu             sync.Mutex
	active         string
	cancel         context.CancelFunc
	settings       backup.Settings
	rsyncInfo      backup.RsyncInfo
	ctx            context.Context
	watchMu        sync.RWMutex
	watcher        WatchStatus
	wake           chan struct{}
	watchRefresh   chan struct{}
	mountEpoch     uint64
	volumeMu       sync.Mutex
	volumeChecked  time.Time
	volumeBusy     bool
	volumeCache    []map[string]any
}

func New(st *store.Store, token, host, web string, dev bool) *Server {
	s := &Server{startedAt: time.Now(), Store: st, Token: token, Host: host, Web: web, Dev: dev, settings: backup.Settings{PollSeconds: 30}, ctx: context.Background(), wake: make(chan struct{}, 1), watchRefresh: make(chan struct{}, 1), watcher: WatchStatus{Backend: "polling", Healthy: true}, mountEpoch: uint64(time.Now().UnixNano())}
	st.Get("settings", "app", &s.settings)
	runs, _ := st.Runs()
	for _, r := range runs {
		if r.FinishedAt != nil {
			st.DB.Exec("DELETE FROM run_jobs WHERE run_id=?", r.ID)
		}
		if r.FinishedAt == nil {
			var jobState string
			if st.DB.QueryRow("SELECT state FROM run_jobs WHERE run_id=?", r.ID).Scan(&jobState) == nil && ((jobState == "queued" && (r.Status == "queued" || isWaitingStatus(r.Status))) || (jobState == "held" && r.Status == "paused")) {
				continue
			}
			now := time.Now().UTC()
			r.FinishedAt = &now
			r.Status = "recoverable"
			r.Stage = "服务重启，未完成快照未发布"
			r.Error = "上次运行中断。可重新预览并运行备份，系统会校验并复用兼容暂存中的完整文件；历史快照保持完整。"
			st.Put("backup_runs", r.ID, r)
		}
	}
	st.DB.Exec("DELETE FROM run_jobs WHERE state='running'")
	s.recoverPreviewJobs()
	s.recoverRestoreJobs()
	return s
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/", s.api)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'")
		if s.Web == "" {
			if _, e := webui.FS().Open("index.html"); e != nil {
				http.Error(w, "Web assets missing. Run make build.", 503)
				return
			}
			http.FileServer(http.FS(webui.FS())).ServeHTTP(w, r)
			return
		}
		path := filepath.Join(s.Web, filepath.Clean("/"+r.URL.Path))
		if r.URL.Path == "/" {
			path = filepath.Join(s.Web, "index.html")
		}
		if _, e := os.Stat(path); os.IsNotExist(e) {
			path = filepath.Join(s.Web, "index.html")
		}
		http.ServeFile(w, r, path)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		if r.Host != s.Host && !(s.Dev && (r.Host == "127.0.0.1:5173" || r.Host == "localhost:5173")) {
			fail(w, 403, "主机校验失败")
			return
		}
		origin := r.Header.Get("Origin")
		if origin != "" && origin != "http://"+s.Host && !(s.Dev && (origin == "http://127.0.0.1:5173" || origin == "http://localhost:5173")) {
			fail(w, 403, "请求来源不受信任")
			return
		}
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			fail(w, 403, "拒绝跨站请求")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(auth), []byte(s.Token)) != 1 {
				fail(w, 401, "访问令牌无效，请使用服务启动时显示的链接")
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}
func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, msg string) {
	reply(w, status, map[string]string{"error": msg})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1024*1024)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		fail(w, 400, "请求格式错误: "+e.Error())
		return false
	}
	if e := d.Decode(new(any)); e != io.EOF {
		fail(w, 400, "请求只能包含一个 JSON 对象")
		return false
	}
	return true
}
func (s *Server) config() backup.Settings { s.mu.Lock(); defer s.mu.Unlock(); return s.settings }
func (s *Server) busy() bool              { s.mu.Lock(); defer s.mu.Unlock(); return s.active != "" }
func (s *Server) api(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/"), "/")
	parts := strings.Split(path, "/")
	method := r.Method
	switch {
	case path == "system/index" && method == "GET":
		status, e := s.Store.IndexStatus(r.Context())
		respond(w, status, e)
	case path == "system/index/scan" && method == "POST":
		var req struct {
			PlanID string `json:"planId"`
		}
		if !decode(w, r, &req) {
			return
		}
		s.mu.Lock()
		if s.active != "" {
			s.mu.Unlock()
			fail(w, 409, "请先完成或取消当前文件任务")
			return
		}
		s.active = "index"
		s.mu.Unlock()
		defer func() { s.mu.Lock(); s.active = ""; s.dispatchLocked(); s.mu.Unlock() }()
		var p backup.Plan
		if e := s.Store.Get("backup_plans", req.PlanID, &p); e != nil {
			fail(w, 404, "计划不存在")
			return
		}
		result, e := backup.ScanIndexed(r.Context(), s.scanPlan(p), s.Store, true)
		respond(w, map[string]any{"scanStats": result.Stats, "entries": len(result.Entries)}, e)
	case path == "system/queue" && method == "GET":
		jobs, err := s.queueRuns()
		respond(w, jobs, err)
	case path == "system/monitor" && method == "GET":
		s.monitor.mu.Lock()
		history := append([]monitorSample(nil), s.monitor.history...)
		monitorError := s.monitor.error
		s.monitor.mu.Unlock()
		reply(w, 200, map[string]any{"samples": history, "error": monitorError, "intervalSeconds": 1})
	case path == "system/setup" && method == "GET":
		report, err := s.setup(r.Context())
		respond(w, report, err)
	case path == "system/status" && method == "GET":
		s.mu.Lock()
		active := s.active
		s.mu.Unlock()
		home, _ := os.UserHomeDir()
		reply(w, 200, map[string]any{"service": "ready", "version": backup.Version, "database": "SQLite · WAL", "rsync": s.cachedRsync(), "activeRunId": active, "home": home, "watcher": s.watchStatus().Backend, "watchStatus": s.watchStatus(), "platform": "macOS", "capabilities": []string{"snapshots", "sha256", "move-reuse", "scheduler", "safe-restore"}})
	case path == "settings" && method == "GET":
		reply(w, 200, s.config())
	case path == "settings" && method == "PATCH":
		var v backup.Settings
		if !decode(w, r, &v) {
			return
		}
		if s.busy() {
			fail(w, 409, "任务运行期间不能修改设置")
			return
		}
		if v.PollSeconds < 10 || v.PollSeconds > 3600 {
			fail(w, 400, "扫描间隔须在 10 到 3600 秒之间")
			return
		}
		rs := backup.DetectRsync(v.RsyncPath)
		if !rs.Available {
			fail(w, 400, "指定路径不是可执行的 rsync")
			return
		}
		v.RsyncPath = rs.Path
		if e := s.Store.Put("settings", "app", v); e != nil {
			fail(w, 500, e.Error())
			return
		}
		s.mu.Lock()
		s.settings = v
		s.rsyncInfo = rs
		s.mu.Unlock()
		reply(w, 200, v)
	case path == "repositories/recovery/preview" && method == "POST":
		s.recoveryAPI(w, r, false)
	case path == "repositories/recovery/apply" && method == "POST":
		s.recoveryAPI(w, r, true)
	case path == "repositories/import" && method == "POST":
		var req struct {
			Path string `json:"path"`
		}
		if !decode(w, r, &req) {
			return
		}
		s.mu.Lock()
		if s.active != "" {
			s.mu.Unlock()
			fail(w, 409, "已有文件任务正在执行")
			return
		}
		s.active = "recovery"
		s.mu.Unlock()
		defer func() { s.mu.Lock(); s.active = ""; s.mu.Unlock() }()
		path, e := backup.Canonical(req.Path)
		if e != nil {
			fail(w, 400, e.Error())
			return
		}
		v, e := backup.VolumeID(path)
		if e != nil {
			fail(w, 400, e.Error())
			return
		}
		result, e := s.importRepository(r.Context(), backup.Destination{Path: path, VolumeID: v}, true)
		respond(w, result, e)
	case path == "volumes" && method == "GET":
		s.volumes(w)
	case parts[0] == "preview-jobs":
		s.previewJobs(w, r, parts)
	case path == "filesystem/directories" && method == "POST":
		s.createDirectory(w, r)
	case path == "filesystem/browse" && method == "GET":
		s.browse(w, r)
	case path == "plans" && method == "GET":
		v, e := s.Store.Plans()
		respond(w, v, e)
	case path == "plans" && method == "POST":
		var p backup.Plan
		if !decode(w, r, &p) {
			return
		}
		p.ID = backup.ID("plan-")
		p.CreatedAt = time.Now().UTC()
		s.savePlan(w, p, true)
	case len(parts) == 2 && parts[0] == "plans":
		var p backup.Plan
		if e := s.Store.Get("backup_plans", parts[1], &p); e != nil {
			fail(w, 404, "计划不存在")
			return
		}
		switch method {
		case "GET":
			reply(w, 200, p)
		case "PATCH":
			if s.busy() {
				fail(w, 409, "任务运行期间不能修改计划")
				return
			}
			var next backup.Plan
			if !decode(w, r, &next) {
				return
			}
			next.ID = p.ID
			next.CreatedAt = p.CreatedAt
			s.savePlan(w, next, false)
		case "DELETE":
			if s.busy() {
				fail(w, 409, "任务运行期间不能删除计划")
				return
			}
			e := s.Store.Delete("backup_plans", p.ID)
			respond(w, map[string]bool{"deleted": true}, e)
		default:
			fail(w, 405, "不支持的请求方法")
		}
	case len(parts) == 3 && parts[0] == "plans":
		var p backup.Plan
		if e := s.Store.Get("backup_plans", parts[1], &p); e != nil {
			fail(w, 404, "计划不存在")
			return
		}
		switch {
		case parts[2] == "schedule" && method == "GET":
			reply(w, 200, s.scheduleStatus(p))
		case parts[2] == "preview-jobs" && method == "POST":
			var req struct {
				SourceIDs []string `json:"sourceIds,omitempty"`
			}
			if !decode(w, r, &req) {
				return
			}
			job, err := s.enqueuePreview(p, req.SourceIDs)
			if err != nil {
				fail(w, 400, err.Error())
				return
			}
			reply(w, 202, job)
		case parts[2] == "preview" && method == "POST":
			var req struct {
				SourceIDs []string `json:"sourceIds,omitempty"`
			}
			if r.ContentLength != 0 && !decode(w, r, &req) {
				return
			}
			v, e := s.createPreview(r.Context(), p, req.SourceIDs)
			respond(w, v, e)
		case parts[2] == "runs" && method == "POST":
			var req RunRequest
			if !decode(w, r, &req) {
				return
			}
			run, e := s.startWithRequest(p, "manual", req)
			if e != nil {
				fail(w, 409, e.Error())
				return
			}
			reply(w, 202, run)
		case parts[2] == "sources" && method == "GET":
			reply(w, 200, p.Sources)
		case parts[2] == "destinations" && method == "GET":
			reply(w, 200, p.Destinations)
		default:
			fail(w, 404, "接口不存在")
		}
	case len(parts) == 4 && parts[0] == "plans" && parts[2] == "rules" && parts[3] == "preview" && method == "POST":
		var p backup.Plan
		if !decode(w, r, &p) {
			return
		}
		p.ID = parts[1]
		if e := backup.Validate(&p); e != nil {
			fail(w, 400, e.Error())
			return
		}
		p = s.scanPlan(p)
		p.EvaluatedAt = backup.EvaluationTime(p)
		result, e := backup.ScanIndexed(r.Context(), p, backup.ReadOnlyIndex(s.Store), false)
		entries, ignored := result.Entries, result.Ignored
		respond(w, map[string]any{"entries": entries, "ignored": ignored, "selectedReasons": backup.ExplainSelection(p, entries), "sources": p.Sources, "evaluatedAt": p.EvaluatedAt, "scanStats": result.Stats}, e)
	case path == "system/source/inspect" && method == "POST":
		var src backup.Source
		if !decode(w, r, &src) {
			return
		}
		observed, err := backup.BindSource(src)
		if backup.SourceBound(src) && strings.HasPrefix(src.MountPath, "/Volumes/") && (src.VolumeRelativePath == "." || backup.SafeRelative(src.VolumeRelativePath)) && src.Path == filepath.Join(src.MountPath, src.VolumeRelativePath) {
			observed, err = backup.ResolveSource(src)
		}
		state := "bound"
		message := "原目录身份匹配"
		if !backup.SourceBound(src) {
			state = "unbound"
			message = "当前目录可用；保存计划后绑定此卷与目录"
		}
		if err != nil {
			state = "unavailable"
			if errors.Is(err, backup.ErrSourceChanged) {
				state = "changed"
			}
			reply(w, 200, map[string]any{"state": state, "message": err.Error()})
			return
		}
		reply(w, 200, map[string]any{"state": state, "message": message, "source": observed})
	case path == "runs" && method == "GET":
		if r.URL.Query().Get("summary") == "true" {
			v, e := s.Store.RunSummaries(r.Context())
			respond(w, v, e)
		} else {
			v, e := s.Store.Runs()
			respond(w, v, e)
		}
	case len(parts) >= 2 && parts[0] == "runs":
		var run backup.Run
		if e := s.Store.Get("backup_runs", parts[1], &run); e != nil {
			fail(w, 404, "运行记录不存在")
			return
		}
		if len(parts) == 2 && method == "GET" {
			reply(w, 200, run)
		} else if len(parts) == 3 && (parts[2] == "cancel" || parts[2] == "pause" || parts[2] == "resume") && method == "POST" {
			result, e := s.controlRun(run.ID, parts[2])
			if e != nil {
				fail(w, 409, e.Error())
				return
			}
			reply(w, 200, result)
		} else if len(parts) == 3 && parts[2] == "operations" && method == "GET" {
			reply(w, 200, run.Preview)
		} else {
			fail(w, 404, "接口不存在")
		}
	case path == "snapshots" && method == "GET":
		out, e := s.Store.SnapshotSummaries(r.Context(), r.URL.Query().Get("planId"))
		respond(w, out, e)
	case len(parts) >= 2 && parts[0] == "snapshots":
		s.snapshotAPI(w, r, parts)
	case path == "restores" && method == "GET":
		s.listRestores(w, r)
	case path == "restores" && method == "POST":
		s.restore(w, r)
	case path == "retention/preview" && method == "POST":
		s.retention(w, r, false)
	case path == "retention/apply" && method == "POST":
		s.retention(w, r, true)
	case path == "events" && method == "GET":
		w.Header().Set("Content-Type", "text/event-stream")
		f, ok := w.(http.Flusher)
		if !ok {
			return
		}
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-t.C:
				runs, _ := s.Store.Runs()
				if len(runs) > 20 {
					runs = runs[:20]
				}
				b, _ := json.Marshal(runs)
				fmt.Fprintf(w, "event: runs\ndata: %s\n\n", b)
				f.Flush()
			}
		}
	default:
		fail(w, 404, "接口不存在")
	}
}
func respond(w http.ResponseWriter, v any, e error) {
	if e != nil {
		fail(w, 400, e.Error())
		return
	}
	reply(w, 200, v)
}
func (s *Server) savePlan(w http.ResponseWriter, p backup.Plan, create bool) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	var previous *backup.Plan
	if !create {
		var old backup.Plan
		if s.Store.Get("backup_plans", p.ID, &old) == nil {
			copy := old
			previous = &copy
			old.Enabled = p.Enabled
			// Schedule-only edits do not rebind a disk and work while it is offline.
			old.Schedule = p.Schedule
			if reflect.DeepEqual(old, p) && allSourcesBound(old) {
				if err := backup.ValidateSchedule(p.Schedule); err != nil {
					fail(w, 400, err.Error())
					return
				}
				p.UpdatedAt = time.Now().UTC()
				e := s.persistPlan(p, create)
				if e == nil {
					select {
					case s.watchRefresh <- struct{}{}:
					default:
					}
				}
				if errors.Is(e, errPlanBusy) {
					fail(w, 409, e.Error())
					return
				}
				respond(w, p, e)
				return
			}
		}
	}

	if e := backup.ValidateForUpdate(&p, previous); e != nil {
		fail(w, 400, e.Error())
		return
	}
	for _, src := range p.Sources {
		if s.Store.Path != "" && backup.Contains(filepath.Dir(s.Store.Path), src.Path) {
			fail(w, 400, "不能将 LocalBackup 服务数据目录作为备份源")
			return
		}
	}
	existing, err := s.Store.Plans()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	for _, other := range existing {
		if other.ID == p.ID {
			continue
		}
		for _, source := range p.Sources {
			for _, otherSource := range other.Sources {
				if strings.EqualFold(source.ID, otherSource.ID) {
					fail(w, 400, "源目录 ID 已属于其他计划，请使用独立 ID")
					return
				}
			}
			for _, target := range other.Destinations {
				if backup.PathsOverlap(source.Path, target.Path) {
					fail(w, 400, "源目录与其他计划的目标目录重叠")
					return
				}
			}
		}
		for _, target := range p.Destinations {
			for _, source := range other.Sources {
				if backup.PathsOverlap(source.Path, target.Path) {
					fail(w, 400, "备份目标与其他计划的源目录重叠")
					return
				}
			}
		}
	}
	p.UpdatedAt = time.Now().UTC()
	if e := s.persistPlan(p, create); e != nil {
		if errors.Is(e, errPlanBusy) {
			fail(w, 409, e.Error())
			return
		}
		fail(w, 500, e.Error())
		return
	}
	select {
	case s.watchRefresh <- struct{}{}:
	default:
	}
	status := 200
	if create {
		status = 201
	}
	reply(w, status, p)
}
func (s *Server) latest(p, d string) *backup.Snapshot {
	v, _ := s.Store.LatestSnapshot(s.ctx, p, d)
	return v
}
func (s *Server) preview(ctx context.Context, p backup.Plan) (backup.Preview, []backup.Entry, error) {
	// Internal audits may explicitly request a full preflight. Execution gates
	// its uncached full preflight in Execute, only when transfer is needed.
	return s.previewWithPreflight(ctx, p, false)
}

func (s *Server) previewWithPreflight(ctx context.Context, p backup.Plan, lightweightPreflight bool) (backup.Preview, []backup.Entry, error) {
	backup.Report(ctx, backup.WorkEvent{Phase: "preview:正在检查源目录与磁盘身份"})
	p = s.scanPlan(p)
	var resolveErr error
	p, resolveErr = s.resolveSources(p)
	if resolveErr != nil {
		return backup.Preview{}, nil, resolveErr
	}
	p.EvaluatedAt = backup.EvaluationTime(p)
	v := backup.Preview{SourceIDs: append([]string(nil), p.ExecutionSourceIDs...), EvaluatedAt: p.EvaluatedAt, Operations: []backup.Operation{}, Removals: []backup.Operation{}, Warnings: []string{"备份反映运行期间的文件状态；运行中的数据库或虚拟机请先关闭或导出。"}}
	if len(p.ExecutionSourceIDs) > 0 {
		v.Warnings = append(v.Warnings, "仅扫描与更新所选目录；其他目录沿用上次备份，尚未备份的目录不会出现在此恢复点中。")
	}
	backup.Report(ctx, backup.WorkEvent{Phase: "preview:正在读取变化索引"})
	p, hintErr := s.withDirtyHints(ctx, p, false)
	if hintErr != nil {
		return v, nil, hintErr
	}
	backup.Report(ctx, backup.WorkEvent{Phase: "preview:正在扫描源目录与校验变化文件"})
	result, e := backup.ScanIndexed(ctx, p, s.Store, false)
	entries := result.Entries
	v.ScanStats = &result.Stats
	if e != nil {
		return v, nil, e
	}
	bases := []any{}
	v.ViewDigests = map[string]string{}
	v.BaselineIDs = map[string]string{}
	v.CurrentIDs = map[string]string{}
	v.BaselineDigests = map[string]string{}
	for _, d := range p.Destinations {
		if !d.Enabled {
			continue
		}
		backup.Report(ctx, backup.WorkEvent{Phase: "preview:正在读取目标快照：" + d.Name})
		if _, e = backup.CheckDestination(d); e != nil {
			return v, nil, fmt.Errorf("目标 %s: %w", d.Name, e)
		}
		prev, e := s.baseline(p, d)
		if p.Mode != "snapshot" {
			current, er := backup.ReadCurrent(d, p.ID)
			if er != nil {
				return v, nil, er
			}
			currentID := ""
			if current != nil {
				currentID = current.ID
			}
			v.CurrentIDs[d.ID] = currentID
			bases = append(bases, struct{ DestinationID, CurrentID string }{d.ID, currentID})
		}

		if e != nil {
			return v, nil, e
		}
		if prev != nil {
			v.BaselineIDs[d.ID] = prev.ID
			v.BaselineDigests[d.ID] = backup.SnapshotDigest(*prev)
			root, err := backup.ValidateSnapshot(*prev)
			if err != nil {
				return v, nil, err
			}
			// Preview compares manifests without reading historical file contents.
			// Execute audits the baseline under the repository lock before reuse.
			if p.Mode != "snapshot" {
				e = backup.VerifyTreeLayout(ctx, root, prev.Entries, prev.Sources, true)
			}
			if e != nil {
				return v, nil, e
			}
			bases = append(bases, struct{ DestinationID, SnapshotID, RepositoryID, SnapshotFingerprint string }{d.ID, prev.ID, prev.RepositoryID, fingerprint(prev)})
		} else {
			v.BaselineDigests[d.ID] = ""
			bases = append(bases, struct{ DestinationID string }{d.ID})
		}
		phase := "preview:正在检查 rsync 工具兼容性"
		if !lightweightPreflight {
			phase = "preview:正在预检查 rsync：" + d.Name
		}
		backup.Report(ctx, backup.WorkEvent{Phase: phase})
		hit, err := s.preflight(ctx, p, d, entries, prev, backup.DetectRsync(s.config().RsyncPath), lightweightPreflight)
		if err != nil {
			return v, nil, err
		}
		if hit {
			v.PreflightCacheHits++
		}
		if e = ctx.Err(); e != nil {
			return v, nil, e
		}
		backup.Report(ctx, backup.WorkEvent{Phase: "preview:正在整理变更清单"})
		view := backup.BuildView(p, entries, prev)
		v.ViewDigests[d.ID] = backup.ViewDigest(view)
		x := backup.DescribeView(view, prev, d.ID, p.Mode)
		v.Operations = append(v.Operations, x.Operations...)
		v.Removals = append(v.Removals, x.Removals...)
		v.Files += x.Files
		v.Bytes += x.Bytes
		v.TransferBytes += x.TransferBytes
		v.Added += x.Added
		v.Modified += x.Modified
		v.Moved += x.Moved
		v.Deleted += x.Deleted
		v.Reused += x.Reused
		v.Retained += x.Retained
		v.RetainedBytes += x.RetainedBytes
		v.DeletePaths += x.DeletePaths
		v.DeleteBytes += x.DeleteBytes
	}
	v.Fingerprint = fingerprint(struct {
		Plan           backup.Plan
		Entries        []backup.Entry
		Bases          []any
		ViewOperations []backup.Operation
		Removals       []backup.Operation
	}{p, entries, bases, v.Operations, v.Removals})
	if p.Mode == "incremental" {
		v.Warnings = append(v.Warnings, "增量同步保留源端已删除或被排除的旧路径；类型冲突由新路径替换，旧版本仍可恢复。")
	}
	if p.Mode == "mirror" {
		v.Warnings = append(v.Warnings, "镜像仅处理已启用源和规则范围；排除项受保护。移除同步视图中的旧路径必须逐次确认，历史快照保留。")
	}
	if !backup.DetectRsync(s.config().RsyncPath).Modern {
		v.Warnings = append(v.Warnings, "系统 openrsync 使用基础元数据备份；完整扩展属性需要 rsync 3.x。")
	}
	return v, entries, nil
}
func (s *Server) start(p backup.Plan, trigger string) (backup.Run, error) {
	return s.startWithRequest(p, trigger, RunRequest{})
}
func (s *Server) execute(ctx context.Context, p backup.Plan, r backup.Run, approved *previewApproval) {
	p = s.scanPlan(p)
	observe := s.observeRun(r.ID)
	ctx = backup.Observe(ctx, observe)
	defer func() {
		observe(backup.WorkEvent{Phase: "done"})
		s.mu.Lock()
		if !isWaitingStatus(r.Status) {
			s.Store.DB.Exec("DELETE FROM run_jobs WHERE run_id=?", r.ID)
		}
		s.active = ""
		s.cancel = nil
		s.control = nil
		s.dispatchLocked()
		s.mu.Unlock()
	}()
	save := func() {
		s.runSaveMu.Lock()
		defer s.runSaveMu.Unlock()
		if isRunningStatus(r.Status) {
			if c := backup.Controller(ctx); c != nil {
				r.Status = c.State()
			}
		}
		if e := s.Store.Put("backup_runs", r.ID, r); e != nil {
			log.Printf("persist run: %v", e)
		}
	}
	logLine := func(msg string) {
		r.Logs = append(r.Logs, time.Now().Format("15:04:05")+"  "+msg)
		if len(r.Logs) > 500 {
			r.Logs = r.Logs[len(r.Logs)-500:]
		}
		r.Stage = msg
		save()
	}
	finish := func(err error) {
		now := time.Now().UTC()
		r.FinishedAt = &now
		if err == nil {
			r.Status = "success"
			r.Progress = 100
			r.Stage = "所有目标备份完成"
			if len(r.SourceIDs) > 0 {
				r.Stage = "所选目录备份完成，其他目录保留上次版本"
			}

			if len(r.DestinationIDs) > 0 {
				r.Stage = "本次连接的目标备份完成"
			}
			r.NoChanges = len(r.CreatedSnapshotIDs) == 0 && len(r.UnchangedDestinationIDs) > 0
			if r.NoChanges {
				r.Stage = "内容未变化，完整校验通过，未新建恢复点"
			}
		} else {
			r.Status = "failed"
			r.Error = err.Error()
			r.Stage = "备份未完成"
			var confirmation confirmationRequired
			if errors.As(err, &confirmation) || errors.Is(err, backup.ErrViewChanged) {
				r.Status = "needs_confirmation"
				r.Stage = "等待重新预览确认"
			}
			if ctx.Err() != nil {
				r.Status = "cancelled"
				r.Stage = "备份已取消"
			}
		}
		save()
	}
	if err := backup.Checkpoint(ctx); err != nil {
		finish(err)
		return
	}
	var current backup.Plan
	if err := s.Store.Get("backup_plans", p.ID, &current); err != nil || fingerprint(current) != fingerprint(p) {
		finish(confirmationRequired{"排队期间计划已变化，请重新预览后运行"})
		return
	}
	p.RecoveryPlan = &current
	if len(r.SourceIDs) > 0 {
		var err error
		p, _, err = scopeSources(p, r.SourceIDs)
		if err != nil {
			finish(confirmationRequired{err.Error()})
			return
		}
		logLine("单独备份目录：" + strings.Join(r.SourceNames, "、"))
	}
	if len(r.DestinationIDs) > 0 {
		var err error
		p, err = scopedMountPlan(p, r)
		if err != nil {
			finish(err)
			return
		}
		logLine("磁盘连接触发，仅执行本次连接的绑定目标")
	}
	if approved != nil && time.Now().After(approved.ExpiresAt) {
		finish(confirmationRequired{"排队期间预览已过期，请重新预览"})
		return
	}

	var sourceErr error
	p, sourceErr = s.resolveSources(p)
	if sourceErr != nil {
		if errors.Is(sourceErr, backup.ErrSourceUnavailable) {
			if err := backup.Checkpoint(ctx); err != nil {
				finish(err)
				return
			}
			r.Status = "waiting_for_source"
			r.Stage = "等待源目录或原磁盘 · " + sourceErr.Error()
			if err := s.persistWaiting(ctx, r); err != nil {
				finish(err)
			}
			return
		}
		finish(confirmationRequired{sourceErr.Error()})
		return
	}
	for _, d := range p.Destinations {
		if d.Enabled {
			if _, err := backup.CheckDestination(d); err != nil {
				if e := backup.Checkpoint(ctx); e != nil {
					finish(e)
					return
				}
				r.Status = "waiting_for_volume"
				r.Stage = "等待目标磁盘 · " + d.Name
				if len(r.Logs) == 0 || r.Logs[len(r.Logs)-1] != err.Error() {
					r.Logs = append(r.Logs, err.Error())
				}
				if len(r.Logs) > 500 {
					r.Logs = r.Logs[len(r.Logs)-500:]
				}
				if e := s.persistWaiting(ctx, r); e != nil {
					finish(e)
				}
				return
			}
		}
	}
	if !p.Enabled && r.Trigger != "manual" {
		finish(errors.New("计划已暂停"))
		return
	}
	rs := backup.DetectRsync(s.config().RsyncPath)
	if !rs.Available {
		finish(errors.New("rsync 不可用"))
		return
	}
	baseline, baselineErr := s.pending(p.ID)
	if baselineErr != nil {
		finish(baselineErr)
		return
	}
	preview, entries, e := s.previewWithPreflight(ctx, p, true)
	if e != nil {
		finish(e)
		return
	}
	r.Preview = &preview
	if e = validateApproval(p, preview, approved, r.DeletesConfirmed); e != nil {
		finish(e)
		return
	}
	p.EvaluatedAt = preview.EvaluatedAt
	p.ExpectedViewDigests = preview.ViewDigests
	p.ExpectedBaselines = preview.BaselineIDs
	p.ExpectedCurrentIDs = preview.CurrentIDs
	p.ExpectedBaselineDigests = preview.BaselineDigests
	p.MirrorDeletesAuthorized = approved != nil && r.DeletesConfirmed
	r.Progress = 15
	save()
	for i, d := range p.Destinations {
		if !d.Enabled {
			continue
		}
		prev, e := s.baseline(p, d)
		if e != nil {
			finish(e)
			return
		}
		snap, e := backup.Execute(ctx, p, d, entries, prev, rs, logLine)
		if e != nil {
			finish(e)
			return
		}
		if snap == nil {
			r.UnchangedDestinationIDs = append(r.UnchangedDestinationIDs, d.ID)
		} else {
			if e = s.Store.Put("snapshots", snap.ID, snap); e != nil {
				finish(e)
				return
			}
			r.CreatedSnapshotIDs = append(r.CreatedSnapshotIDs, snap.ID)
		}
		r.Progress = 15 + (i+1)*80/len(p.Destinations)
		save()
	}
	changes, e := s.Store.SyncFileEntries(ctx, p.Sources, entries)
	if e == nil {
		logLine(fmt.Sprintf("文件目录索引更新完成：新增 %d，更新 %d，删除 %d，未变化 %d", changes.Added, changes.Updated, changes.Deleted, changes.Unchanged))
	}
	if e == nil && len(r.SourceIDs) == 0 {
		e = s.ackChanges(p.ID, baseline.Revision)
	}
	finish(e)
}
func (s *Server) restore(w http.ResponseWriter, r *http.Request) {
	var req backup.RestoreRequest
	if !decode(w, r, &req) {
		return
	}
	var snap backup.Snapshot
	if e := s.Store.Get("snapshots", req.SnapshotID, &snap); e != nil {
		fail(w, 404, "快照不存在")
		return
	}
	s.mu.Lock()
	if s.active != "" {
		s.mu.Unlock()
		fail(w, 409, "已有文件任务运行中")
		return
	}
	s.active = "restore"
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.active = ""; s.mu.Unlock() }()
	plans, err := s.Store.Plans()
	if err != nil {
		fail(w, 500, "无法读取计划以校验恢复位置："+err.Error())
		return
	}
	id := backup.ID("restore-")
	job := map[string]any{"id": id, "type": "restore", "status": "running", "request": req, "startedAt": time.Now().UTC()}
	if e := s.Store.Put("restore_jobs", id, job); e != nil {
		fail(w, 500, e.Error())
		return
	}
	path, e := backup.RestoreWithOutputAudit(r.Context(), snap, req, plans, backup.DetectRsync(s.config().RsyncPath), func(path string) error {
		job["path"] = path
		return s.Store.Put("restore_jobs", id, job)
	})
	job["status"] = "success"
	job["path"] = path
	job["finishedAt"] = time.Now().UTC()
	if e != nil {
		job["status"] = "failed"
		job["error"] = e.Error()
		if r.Context().Err() != nil {
			job["status"] = "cancelled"
		}
	}
	if saveErr := s.Store.Put("restore_jobs", id, job); saveErr != nil {
		fail(w, 500, fmt.Sprintf("恢复任务 %s 已结束，但无法保存结果。输出位置：%s；请检查文件后再决定是否重试。审计错误：%v", id, path, saveErr))
		return
	}
	respond(w, job, e)
}
func (s *Server) snapshotAPI(w http.ResponseWriter, r *http.Request, parts []string) {
	var snap backup.Snapshot
	if e := s.Store.Get("snapshots", parts[1], &snap); e != nil {
		fail(w, 404, "快照不存在")
		return
	}
	if len(parts) == 2 && r.Method == "PATCH" {
		var req struct {
			Pinned bool `json:"pinned"`
		}
		if !decode(w, r, &req) {
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.active != "" {
			fail(w, 409, "文件任务执行期间不能修改快照")
			return
		}
		if err := backup.SaveSnapshotPin(snap, req.Pinned); err != nil {
			fail(w, 400, err.Error())
			return
		}
		snap.Pinned = req.Pinned
		respond(w, snap, s.Store.Put("snapshots", snap.ID, snap))
		return
	}
	if len(parts) == 3 && parts[2] == "files" && r.Method == "GET" {
		if _, e := backup.ValidateSnapshot(snap); e != nil {
			fail(w, 400, e.Error())
			return
		}
		source := r.URL.Query().Get("sourceId")
		search := strings.ToLower(r.URL.Query().Get("search"))
		out := []backup.Entry{}
		total := 0
		for _, en := range snap.Entries {
			if (source == "" || en.SourceID == source) && strings.Contains(strings.ToLower(en.Path), search) {
				total++
				if len(out) < 2000 {
					out = append(out, en)
				}
			}
		}
		reply(w, 200, map[string]any{"entries": out, "total": total})
		return
	}
	fail(w, 404, "接口不存在")
}
func (s *Server) retention(w http.ResponseWriter, r *http.Request, apply bool) {
	if apply {
		s.mu.Lock()
		if s.active != "" {
			s.mu.Unlock()
			fail(w, 409, "已有文件任务正在执行")
			return
		}
		s.active = "retention"
		s.mu.Unlock()
		defer func() { s.mu.Lock(); s.active = ""; s.mu.Unlock() }()
	}

	var req struct {
		PlanID  string   `json:"planId"`
		IDs     []string `json:"ids"`
		Confirm bool     `json:"confirm"`
	}
	if !decode(w, r, &req) {
		return
	}
	var p backup.Plan
	if e := s.Store.Get("backup_plans", req.PlanID, &p); e != nil {
		fail(w, 404, "计划不存在")
		return
	}
	all, e := s.Store.Snapshots()
	if e != nil {
		fail(w, 500, e.Error())
		return
	}
	groups := map[string][]backup.Snapshot{}
	for _, sn := range all {
		if sn.PlanID == p.ID {
			groups[sn.DestinationID] = append(groups[sn.DestinationID], sn)
		}
	}
	candidates := []backup.Snapshot{}
	for _, list := range groups {
		currentID := ""
		if len(list) > 0 {
			sn := list[0]
			current, e := backup.ReadCurrent(backup.Destination{ID: sn.DestinationID, Path: sn.DestinationPath, VolumeID: sn.VolumeID, MountPath: sn.MountPath, VolumeRelativePath: sn.VolumeRelativePath}, p.ID)
			if e != nil {
				fail(w, 400, e.Error())
				return
			}
			if current != nil {
				currentID = current.ID
			}
		}
		for _, sn := range backup.RetentionCandidates(list, p.Retention, currentID, time.Now()) {
			sn.Entries = nil
			candidates = append(candidates, sn)
		}
	}
	if !apply {
		reply(w, 200, candidates)
		return
	}
	if !req.Confirm {
		fail(w, 400, "清理需要明确确认")
		return
	}
	allowed := map[string]backup.Snapshot{}
	for _, sn := range candidates {
		allowed[sn.ID] = sn
	}
	for _, id := range req.IDs {
		if _, ok := allowed[id]; !ok {
			fail(w, 409, "清理预览已变化，请重新预览")
			return
		}
	}
	for _, id := range req.IDs {
		sn := allowed[id]
		d := backup.Destination{ID: sn.DestinationID, Path: sn.DestinationPath, VolumeID: sn.VolumeID, MountPath: sn.MountPath, VolumeRelativePath: sn.VolumeRelativePath}
		unlock, e := backup.LockRepository(d, true, false)
		if e != nil {
			fail(w, 409, e.Error())
			return
		}
		root, e := backup.ValidateSnapshot(sn)
		if e != nil {
			unlock()
			fail(w, 400, e.Error())
			return
		}
		current, e := backup.ReadCurrent(d, p.ID)
		if e != nil {
			unlock()
			fail(w, 400, e.Error())
			return
		}
		if current != nil && current.ID == id {
			unlock()
			fail(w, 409, "当前同步视图已变化，请重新预览清理")
			return
		}
		e = os.RemoveAll(root)
		if e == nil {
			e = s.Store.Delete("snapshots", id)
		}
		unlock()
		if e != nil {
			fail(w, 500, e.Error())
			return
		}
	}

	reply(w, 200, map[string]int{"deleted": len(req.IDs)})
}
func (s *Server) browse(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		path, _ = os.UserHomeDir()
	}
	p, e := backup.Canonical(path)
	if e != nil {
		fail(w, 400, e.Error())
		return
	}
	rows, e := backup.ReadDirectory(r.Context(), p)
	if e != nil {
		fail(w, 400, e.Error())
		return
	}
	out := []map[string]any{}
	for _, d := range rows {
		if d.IsDir() && !strings.HasPrefix(d.Name(), ".") {
			out = append(out, map[string]any{"name": d.Name(), "path": filepath.Join(p, d.Name())})
		}
	}
	identity, e := backup.BindSource(backup.Source{Path: p})
	if e != nil {
		fail(w, 400, e.Error())
		return
	}
	reply(w, 200, map[string]any{"path": p, "parent": filepath.Dir(p), "identity": identity, "directories": out})
}
func (s *Server) volumes(w http.ResponseWriter) {
	plans, err := s.Store.Plans()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	s.volumeMu.Lock()
	out := make([]map[string]any, 0, len(s.volumeCache))
	for _, v := range s.volumeCache {
		copy := map[string]any{}
		for k, value := range v {
			copy[k] = value
		}
		out = append(out, copy)
	}
	if !s.volumeBusy && time.Since(s.volumeChecked) >= 10*time.Second {
		s.volumeBusy = true
		go func() {
			discovered := discoverVolumes(plans)
			s.volumeMu.Lock()
			s.volumeCache = discovered
			s.volumeChecked = time.Now()
			s.volumeBusy = false
			s.volumeMu.Unlock()
		}()
	}
	s.volumeMu.Unlock()
	reply(w, 200, mergeVolumeBindings(out, plans))
}

// Discovery is shared across tabs. It is display-only: all writes still perform
// fresh volume/repository validation, independent of this short-lived cache.
func discoverVolumes(plans []backup.Plan) []map[string]any {
	paths := []string{"/"}
	// APFS Data is a separate UUID from the read-only system root.
	for _, plan := range plans {
		for _, d := range plan.Destinations {
			if d.MountPath != "" {
				paths = append(paths, d.MountPath)
			}
		}
	}
	rows, _ := os.ReadDir("/Volumes")
	for _, r := range rows {
		p := filepath.Join("/Volumes", r.Name())
		if r.IsDir() {
			paths = append(paths, p)
		}
	}
	out := []map[string]any{}
	seen := map[string]bool{}
	pathSeen := map[string]bool{}
	for _, p := range paths {
		if pathSeen[p] {
			continue
		}
		pathSeen[p] = true
		v, e := backup.VolumeID(p)
		if e != nil || seen[v] {
			continue
		}
		seen[v] = true
		capacity, free := diskSpace(p)
		name := filepath.Base(p)
		if p == "/" {
			name = "Macintosh HD"
		}
		out = append(out, map[string]any{"id": v, "name": name, "path": p, "capacity": capacity, "free": free, "connected": true})
	}
	return out
}

func (s *Server) persistPlan(p backup.Plan, create bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !create && s.active != "" {
		return errPlanBusy
	}
	var old backup.Plan
	rearm := create
	sourceChanged := false
	scheduleChanged := false
	if s.Store.Get("backup_plans", p.ID, &old) == nil {
		sourceChanged = !reflect.DeepEqual(old.Sources, p.Sources)
		scheduleChanged = !reflect.DeepEqual(old.Schedule, p.Schedule)
		rearm = (!old.Enabled && p.Enabled) || (!old.Schedule.OnMount && p.Schedule.OnMount)
	}
	if err := s.Store.Put("backup_plans", p.ID, p); err != nil {
		return err
	}
	if rearm || sourceChanged || scheduleChanged {
		var cp checkpoint
		s.Store.Get("scan_checkpoints", p.ID, &cp)
		if rearm {
			cp.TargetOnline = nil
			cp.LastVolumeCheck = time.Time{}
		}
		if sourceChanged || scheduleChanged {
			cp.MaxBackupPending = false
			cp.RetryPending = false
			cp.RetryDestinationIDs = nil
			cp.Failures = 0
			cp.LastAttempt = time.Time{}
			cp.LastChangeCheck = time.Time{}
			cp.ChangeSignature = ""
			cp.Changes = nil
		}
		if sourceChanged {
			cp.Failures = 0
			cp.LastAttempt = time.Time{}
			cp.LastAudit = time.Time{}
			cp.Signature = ""
			cp.ChangedAt = time.Time{}
		}
		return s.Store.Put("scan_checkpoints", p.ID, cp)
	}
	return nil
}
