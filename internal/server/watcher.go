package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"localbackup/internal/adapters/fsevents"
	"localbackup/internal/backup"
)

type watchCursor struct {
	EventID   uint64    `json:"eventId"`
	Roots     []string  `json:"roots"`
	UpdatedAt time.Time `json:"updatedAt"`
}
type WatchStatus struct {
	Backend     string    `json:"backend"`
	Healthy     bool      `json:"healthy"`
	Roots       int       `json:"roots"`
	Cursor      uint64    `json:"cursor"`
	Pending     int       `json:"pending"`
	LastEventAt time.Time `json:"lastEventAt"`
	Error       string    `json:"error,omitempty"`
}
type pendingChanges struct {
	Revision   int64
	Count      int
	ChangedAt  time.Time
	NeedsScan  bool
	HasChanges bool
}

func (s *Server) pending(planID string) (pendingChanges, error) {
	var p pendingChanges
	var at int64
	var scan, actual int
	e := s.Store.DB.QueryRow("SELECT COALESCE(MAX(id),0),COUNT(*),COALESCE(MAX(changed_at_ns),0),COALESCE(MAX(requires_scan),0),COALESCE(MAX(CASE WHEN requires_scan=0 THEN 1 ELSE 0 END),0) FROM change_events WHERE plan_id=?", planID).Scan(&p.Revision, &p.Count, &at, &scan, &actual)
	if at != 0 {
		p.ChangedAt = time.Unix(0, at)
	}
	p.NeedsScan = scan != 0
	p.HasChanges = actual != 0
	return p, e
}
func (s *Server) ackChanges(planID string, revision int64) error {
	ctx := context.Background()
	tx, e := s.Store.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	valid, e := s.Store.VerifyWatchState(ctx, tx)
	if e != nil {
		return e
	}
	if !valid {
		tx.Rollback()
		plans, err := s.Store.Plans()
		if err != nil {
			return err
		}
		for i := range plans {
			plans[i] = s.scanPlan(plans[i])
		}
		return s.recordEvents(plans, []fsevents.Event{{Path: "/", Flags: fsevents.MustScanSubDirs}}, true)
	}
	if _, e = tx.ExecContext(ctx, "DELETE FROM change_events WHERE plan_id=? AND id<=?", planID, revision); e != nil {
		return e
	}
	if e = s.Store.SealWatchState(ctx, tx); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Server) watchStatus() WatchStatus {
	s.watchMu.RLock()
	v := s.watcher
	s.watchMu.RUnlock()
	s.Store.DB.QueryRow("SELECT COUNT(*) FROM change_events").Scan(&v.Pending)
	return v
}
func (s *Server) nativeHealthy() bool {
	s.watchMu.RLock()
	defer s.watchMu.RUnlock()
	return s.watcher.Backend == "fsevents" && s.watcher.Healthy
}
func (s *Server) wakeScheduler() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func selectedEvent(p backup.Plan, src backup.Source, path string) bool {
	for _, excluded := range p.RuntimeExcludes {
		if backup.Contains(excluded, path) {
			return false
		}
	}
	rel, e := filepath.Rel(src.Path, path)
	if e != nil || !backup.Contains(src.Path, path) {
		return false
	}
	if rel == "." {
		return true
	}
	selector, e := backup.CompileSelection(p, src)
	if e != nil {
		return false
	}
	return selector.Possible(filepath.ToSlash(rel))
}

// Events and their cursor commit in one transaction. A crash cannot acknowledge
// a journal event without retaining its dirty path. REPLACE advances the revision,
// so an event arriving during a backup survives acknowledgement of its baseline.
func (s *Server) recordEvents(plans []backup.Plan, events []fsevents.Event, startup bool) error {
	tx, e := s.Store.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	valid, e := s.Store.VerifyWatchState(context.Background(), tx)
	if e != nil {
		return e
	}
	if !valid {
		startup = true
		// Even an empty/HistoryDone-only batch must retain the lost queue.
		events = append(events, fsevents.Event{Path: "/", Flags: fsevents.MustScanSubDirs})
	}
	var cursor watchCursor
	s.StoreCursor(tx, &cursor)
	now := time.Now().UTC()
	for _, ev := range events {
		if ev.Flags&fsevents.EventIDsWrapped != 0 {
			cursor.EventID = ev.ID
		}
		if ev.ID > cursor.EventID {
			cursor.EventID = ev.ID
		}
		if ev.Flags&fsevents.HistoryDone != 0 {
			continue
		}
		global := ev.GlobalRescan() || startup
		for _, p := range plans {
			if !p.Enabled || !p.Schedule.OnChange {
				continue
			}
			for _, src := range p.Sources {
				if !src.Enabled {
					continue
				}
				path := filepath.Clean(ev.Path)
				ignoredRuntime := false
				for _, excluded := range p.RuntimeExcludes {
					if backup.Contains(excluded, path) {
						ignoredRuntime = true
						break
					}
				}
				if !global && ignoredRuntime {
					continue
				}
				force := ev.NeedsRescan() || global
				related := backup.Contains(src.Path, path) || (force && backup.Contains(path, src.Path))
				if !global && !related {
					continue
				}
				if !global && !force && !selectedEvent(p, src, path) {
					continue
				}
				rel := "."
				if !global && backup.Contains(src.Path, path) {
					rel, _ = filepath.Rel(src.Path, path)
				}
				_, e = tx.Exec("INSERT OR REPLACE INTO change_events(plan_id,source_id,relative_path,event_id,flags,requires_scan,changed_at_ns) VALUES(?,?,?,?,?,?,?)", p.ID, src.ID, filepath.ToSlash(rel), fmt.Sprint(ev.ID), ev.Flags, force, now.UnixNano())
				if e != nil {
					return e
				}
			}
		}
	}
	cursor.UpdatedAt = now
	b, _ := json.Marshal(cursor)
	if _, e = tx.Exec("INSERT INTO watch_cursors VALUES('host',?) ON CONFLICT(id) DO UPDATE SET data=excluded.data", string(b)); e != nil {
		return e
	}
	if e = s.Store.SealWatchState(context.Background(), tx); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	s.watchMu.Lock()
	s.watcher.Cursor = cursor.EventID
	s.watcher.LastEventAt = now
	s.watchMu.Unlock()
	s.wakeScheduler()
	return nil
}

// StoreCursor must use the existing transaction: SQLite has a single writer
// connection and opening a second query through the pool here would deadlock.
func (s *Server) StoreCursor(tx *sql.Tx, c *watchCursor) {
	var b string
	if tx.QueryRow("SELECT data FROM watch_cursors WHERE id='host'").Scan(&b) == nil {
		_ = json.Unmarshal([]byte(b), c)
	}
}
func watchRoots(plans []backup.Plan) []string {
	set := map[string]bool{}
	for _, p := range plans {
		if p.Enabled && p.Schedule.OnChange {
			for _, src := range p.Sources {
				if src.Enabled {
					set[src.Path] = true
				}
			}
		}
	}
	roots := make([]string, 0, len(set))
	for p := range set {
		roots = append(roots, p)
	}
	sort.Strings(roots)
	return roots
}
func (s *Server) watchLoop(ctx context.Context) {
	if !fsevents.Available() {
		s.watchMu.Lock()
		s.watcher = WatchStatus{Backend: "polling", Healthy: true}
		s.watchMu.Unlock()
		return
	}
	go func() {
		if e := fsevents.WatchMounts(ctx, func() {
			s.watchMu.Lock()
			s.mountEpoch++
			s.watchMu.Unlock()
			s.wakeScheduler()
			select {
			case s.watchRefresh <- struct{}{}:
			default:
			}
		}); e != nil {
			log.Printf("mount directory watcher: %v", e)
		}
	}()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	batches := make(chan []fsevents.Event, 128)
	var stream fsevents.Stream
	defer func() {
		if stream != nil {
			stream.Stop()
		}
	}()
	var roots []string
	var configuration string
	var plans []backup.Plan
	reconcile := func() {
		current, e := s.Store.Plans()
		if e != nil {
			return
		}
		for i := range current {
			current[i] = s.scanPlan(current[i])
		}
		next := watchRoots(current)
		if len(next) == 0 {
			if stream != nil {
				stream.Stop()
				stream = nil
			}
			roots = nil
			plans = current
			s.watchMu.Lock()
			s.watcher = WatchStatus{Backend: "fsevents", Healthy: true}
			s.watchMu.Unlock()
			return
		}

		encoded, _ := json.Marshal(current)
		nextConfiguration := string(encoded)
		same := strings.Join(roots, "\x00") == strings.Join(next, "\x00") && configuration == nextConfiguration
		plans = current
		if same && stream != nil && s.nativeHealthy() {
			return
		}
		if stream != nil {
			stream.Stop()
			stream = nil
		}
		var cursor watchCursor
		s.Store.Get("watch_cursors", "host", &cursor)
		latest := fsevents.Current()
		if cursor.EventID == 0 || cursor.EventID > latest {
			cursor.EventID = latest
		}
		cursor.Roots = next
		cursor.UpdatedAt = time.Now().UTC()
		if e = s.Store.Put("watch_cursors", "host", cursor); e != nil {
			return
		}
		if e = s.recordEvents(current, []fsevents.Event{{Path: "/", ID: cursor.EventID, Flags: fsevents.MustScanSubDirs}}, true); e != nil {
			return
		}
		stream, e = fsevents.Start(next, cursor.EventID, func(events []fsevents.Event) {
			select {
			case batches <- events:
			case <-ctx.Done():
			}
		})
		s.watchMu.Lock()
		s.watcher.Backend = "fsevents"
		s.watcher.Healthy = e == nil
		s.watcher.Roots = len(next)
		s.watcher.Error = ""
		if e != nil {
			s.watcher.Error = e.Error()
		}
		s.watchMu.Unlock()
		if e != nil {
			log.Printf("FSEvents unavailable, using polling: %v", e)
			return
		}
		roots = next
		configuration = nextConfiguration
		s.wakeScheduler()
	}
	reconcile()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.watchRefresh:
			reconcile()
		case <-ticker.C:
			reconcile()
		case events := <-batches:
			if e := s.recordEvents(plans, events, false); e != nil {
				s.watchMu.Lock()
				s.watcher.Healthy = false
				s.watcher.Error = e.Error()
				s.watchMu.Unlock()
				log.Printf("persist FSEvents: %v", e)
			}
		}
	}
}

// Never back up the live index, access token or WAL into a source snapshot.
// This also prevents self-generated database events from sustaining a backup loop.
func (s *Server) scanPlan(p backup.Plan) backup.Plan {
	if s.Store.Path != "" {
		p.RuntimeExcludes = []string{filepath.Dir(s.Store.Path)}
	}
	return p
}
