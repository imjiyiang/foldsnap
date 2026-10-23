package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"localbackup/internal/adapters/fsevents"
	"localbackup/internal/backup"
)

func TestDurableChangesCursorAndBaselineAcknowledgement(t *testing.T) {
	s := testServer(t)
	p := planFixture(t)
	p.ID = "plan-events"
	p.Schedule.OnChange = true
	if e := backup.Validate(&p); e != nil {
		t.Fatal(e)
	}
	p.Rules = []backup.Rule{{Action: "exclude", Pattern: "*.tmp"}}
	events := []fsevents.Event{{Path: filepath.Join(p.Sources[0].Path, "one.txt"), ID: 100}, {Path: filepath.Join(p.Sources[0].Path, "ignored.tmp"), ID: 101}}
	if e := s.recordEvents([]backup.Plan{p}, events, false); e != nil {
		t.Fatal(e)
	}
	baseline, e := s.pending(p.ID)
	if e != nil || baseline.Count != 1 {
		t.Fatalf("bad pending %+v %v", baseline, e)
	}
	var cursor watchCursor
	if e = s.Store.Get("watch_cursors", "host", &cursor); e != nil || cursor.EventID != 101 {
		t.Fatalf("cursor not durable %+v %v", cursor, e)
	}
	if e = s.recordEvents([]backup.Plan{p}, []fsevents.Event{{Path: events[0].Path, ID: 102}}, false); e != nil {
		t.Fatal(e)
	}
	s.ackChanges(p.ID, baseline.Revision)
	next, _ := s.pending(p.ID)
	if next.Count != 1 || next.Revision <= baseline.Revision {
		t.Fatal("new event lost when acknowledging backup baseline")
	}
	s.ackChanges(p.ID, next.Revision)
	next, _ = s.pending(p.ID)
	if next.Count != 0 {
		t.Fatal("baseline not acknowledged")
	}
	if e = s.recordEvents([]backup.Plan{p}, []fsevents.Event{{Path: "/", ID: 103, Flags: fsevents.UserDropped | fsevents.MustScanSubDirs}}, false); e != nil {
		t.Fatal(e)
	}
	next, _ = s.pending(p.ID)
	if next.Count != 2 || !next.NeedsScan {
		t.Fatal("dropped events did not dirty all sources")
	}
}
func TestJournalLossRescansContentWithSameMetadata(t *testing.T) {
	s := testServer(t)
	p := planFixture(t)
	p.ID = "plan-gap"
	p.Schedule = backup.Schedule{OnChange: true, DebounceSeconds: 10}
	if e := backup.Validate(&p); e != nil {
		t.Fatal(e)
	}
	entries, _, e := backup.Scan(context.Background(), p)
	if e != nil {
		t.Fatal(e)
	}
	sn, e := backup.Execute(context.Background(), p, p.Destinations[0], entries, nil, backup.DetectRsync(""), func(string) {})
	if e != nil {
		t.Fatal(e)
	}
	s.Store.Put("snapshots", sn.ID, sn)
	s.Store.Put("backup_plans", p.ID, p)
	sig, _ := signature(context.Background(), p)
	s.Store.Put("scan_checkpoints", p.ID, checkpoint{Signature: sig, Online: true, LastAudit: time.Now()})
	path := filepath.Join(p.Sources[0].Path, "one.txt")
	st, _ := os.Stat(path)
	if e = os.WriteFile(path, []byte("same-length!"), 0644); e != nil {
		t.Fatal(e)
	}
	os.Chtimes(path, st.ModTime(), st.ModTime())
	latest, _ := os.Stat(path)
	if latest.Size() != st.Size() || !latest.ModTime().Equal(st.ModTime()) {
		t.Fatal("fixture must preserve size and mtime")
	}
	// ctime is now part of the polling hint. Model an already-observed cheap
	// signature whose content/index work was lost before a journal-gap audit.
	sig2, _ := signature(context.Background(), p)
	var checkpointState checkpoint
	s.Store.Get("scan_checkpoints", p.ID, &checkpointState)
	checkpointState.Signature = sig2
	s.Store.Put("scan_checkpoints", p.ID, checkpointState)
	if e = s.recordEvents([]backup.Plan{p}, []fsevents.Event{{Path: "/", ID: 200, Flags: fsevents.KernelDropped | fsevents.MustScanSubDirs}}, false); e != nil {
		t.Fatal(e)
	}
	s.Store.DB.Exec("UPDATE change_events SET changed_at_ns=?", time.Now().Add(-time.Minute).UnixNano())
	s.watchMu.Lock()
	s.watcher = WatchStatus{Backend: "fsevents", Healthy: true}
	s.watchMu.Unlock()
	s.tick(context.Background())
	deadline := time.Now().Add(15 * time.Second)
	for s.busy() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	runs, _ := s.Store.Runs()
	if len(runs) != 1 || runs[0].Status != "success" {
		t.Fatalf("journal gap did not recover changed content %+v", runs)
	}
}
func TestExcludedParentAlwaysWinsForEvents(t *testing.T) {
	p := planFixture(t)
	p.Rules = []backup.Rule{{Action: "include", Pattern: "**/cache/keep.txt"}, {Action: "exclude", Pattern: "**/cache/**"}}
	if selectedEvent(p, p.Sources[0], filepath.Join(p.Sources[0].Path, "cache/keep.txt")) {
		t.Fatal("event bypassed scanner's subtree pruning")
	}
}

func TestLiveNativeWatcherPersistsSelectedEvent(t *testing.T) {
	if !fsevents.Available() {
		t.Skip("native macOS FSEvents unavailable")
	}
	s := testServer(t)
	p := planFixture(t)
	p.ID = "plan-native"
	p.Schedule.OnChange = true
	if e := backup.Validate(&p); e != nil {
		t.Fatal(e)
	}
	s.Store.Put("backup_plans", p.ID, p)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.watchLoop(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("watcher shutdown timed out")
		}
	}()
	deadline := time.Now().Add(8 * time.Second)
	for !s.nativeHealthy() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !s.nativeHealthy() {
		t.Fatalf("native stream not started: %+v", s.watchStatus())
	}
	pending, _ := s.pending(p.ID)
	s.ackChanges(p.ID, pending.Revision)
	baseline := s.watchStatus().Cursor
	path := filepath.Join(p.Sources[0].Path, "native-change.txt")
	if e := os.WriteFile(path, []byte("native durable event"), 0644); e != nil {
		t.Fatal(e)
	}
	for time.Now().Before(deadline) {
		pending, _ = s.pending(p.ID)
		if pending.HasChanges && s.watchStatus().Cursor > baseline {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("native event not durably queued: %+v %+v", pending, s.watchStatus())
}

func TestServiceMetadataExcludedFromScanAndEvents(t *testing.T) {
	s := testServer(t)
	base := filepath.Dir(s.Store.Path)
	source := filepath.Dir(base)
	p := backup.Plan{ID: "plan-private", Sources: []backup.Source{{ID: "src-private", Name: "Parent", Path: source, Enabled: true}}}
	bound, err := backup.BindSource(p.Sources[0])
	if err != nil {
		t.Fatal(err)
	}
	p.Sources[0] = bound
	source = bound.Path
	p = s.scanPlan(p)
	entries, ignored, e := backup.Scan(context.Background(), p)
	if e != nil {
		t.Fatal(e)
	}
	for _, en := range entries {
		if backup.Contains(base, filepath.Join(source, en.Path)) {
			t.Fatal("service database or access token entered snapshot")
		}
	}
	if len(ignored) == 0 {
		t.Fatal("service directory was not reported excluded")
	}
	if selectedEvent(p, p.Sources[0], s.Store.Path) {
		t.Fatal("database event can trigger backup loop")
	}
	serviceRoot, err := backup.BindSource(backup.Source{ID: "src-private", Name: "Service", Path: base, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	p.Sources[0] = serviceRoot
	if _, _, e = backup.Scan(context.Background(), p); e == nil {
		t.Fatal("service directory accepted as backup source")
	}
}
