package server

import (
	"context"
	"localbackup/internal/adapters/fsevents"
	"localbackup/internal/backup"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func nativeHintsFixture(t *testing.T) (*Server, backup.Plan) {
	t.Helper()
	s := testServer(t)
	p := planFixture(t)
	p.ID = "plan-subtree"
	p.Schedule.OnChange = true
	if err := backup.Validate(&p); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.Put("backup_plans", p.ID, p); err != nil {
		t.Fatal(err)
	}
	s.Store.Put("watch_cursors", "host", watchCursor{Roots: watchRoots([]backup.Plan{p}), EventID: 1})
	if err := s.recordEvents([]backup.Plan{p}, []fsevents.Event{{Path: "/", Flags: fsevents.MustScanSubDirs}}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.ScanIndexed(context.Background(), s.scanPlan(p), s.Store, true); err != nil {
		t.Fatal(err)
	}
	pending, _ := s.pending(p.ID)
	if err := s.ackChanges(p.ID, pending.Revision); err != nil {
		t.Fatal(err)
	}
	s.watchMu.Lock()
	s.watcher = WatchStatus{Backend: "fsevents", Healthy: true}
	s.watchMu.Unlock()
	return s, p
}
func TestNativeHintsRequireSealedQueueAndCoveredRoots(t *testing.T) {
	s, p := nativeHintsFixture(t)
	hints, err := s.withDirtyHints(context.Background(), p, false)
	if err != nil || len(hints.RuntimeDirtyRoots) != 2 {
		t.Fatal(hints.RuntimeDirtyRoots, err)
	}
	path := filepath.Join(p.Sources[0].Path, "nested/a.txt")
	if err = s.recordEvents([]backup.Plan{p}, []fsevents.Event{{Path: path, ID: 2}}, false); err != nil {
		t.Fatal(err)
	}
	hints, err = s.withDirtyHints(context.Background(), p, false)
	if err != nil || len(hints.RuntimeDirtyRoots[p.Sources[0].ID]) != 1 || hints.RuntimeDirtyRoots[p.Sources[0].ID][0] != "nested/a.txt" {
		t.Fatal(hints.RuntimeDirtyRoots, err)
	}
	s.Store.DB.Exec("DELETE FROM change_events")
	hints, err = s.withDirtyHints(context.Background(), p, false)
	if err != nil || hints.RuntimeDirtyRoots != nil {
		t.Fatal("deleted event trusted", err)
	}
	// Acknowledgement must reseed every plan/source instead of blessing the loss.
	if err = s.ackChanges(p.ID, 100); err != nil {
		t.Fatal(err)
	}
	pending, _ := s.pending(p.ID)
	if !pending.NeedsScan || pending.Count != 2 {
		t.Fatal("lost events not recovered", pending)
	}
	hints, _ = s.withDirtyHints(context.Background(), p, false)
	if len(hints.RuntimeDirtyRoots[p.Sources[0].ID]) != 1 || hints.RuntimeDirtyRoots[p.Sources[0].ID][0] != "." {
		t.Fatal(hints.RuntimeDirtyRoots)
	}
	s.Store.Put("watch_cursors", "host", watchCursor{EventID: 3, Roots: []string{p.Sources[1].Path}})
	hints, _ = s.withDirtyHints(context.Background(), p, false)
	if hints.RuntimeDirtyRoots != nil {
		t.Fatal("cursor tamper trusted")
	}
	// Reconcile legitimately seals new coverage, but does not lend a missing root.
	s.recordEvents([]backup.Plan{p}, nil, false)
	hints, _ = s.withDirtyHints(context.Background(), p, false)
	if _, ok := hints.RuntimeDirtyRoots[p.Sources[0].ID]; ok {
		t.Fatal("unwatched root trusted")
	}
	for _, force := range []bool{true, false} {
		copy := p
		if !force {
			copy.Enabled = false
		}
		hints, _ = s.withDirtyHints(context.Background(), copy, force)
		if hints.RuntimeDirtyRoots != nil {
			t.Fatal("forced/disabled plan reused hints")
		}
	}
}
func TestNativePreviewUsesDirtySubtreeAndQueueLossFallsBack(t *testing.T) {
	s, p := nativeHintsFixture(t)
	root := p.Sources[0].Path
	os.MkdirAll(filepath.Join(root, "hot/nested"), 0755)
	os.MkdirAll(filepath.Join(root, "cold/nested"), 0755)
	os.WriteFile(filepath.Join(root, "hot/nested/a.txt"), []byte("old"), 0644)
	os.WriteFile(filepath.Join(root, "cold/nested/b.txt"), []byte("cold"), 0644)
	full, err := backup.ScanIndexed(context.Background(), s.scanPlan(p), s.Store, true)
	if err != nil {
		t.Fatal(err)
	}
	projection, _ := s.Store.LoadSourceProjection(context.Background(), p.Sources[0].ID)
	if projection == nil || !projection.Scope.Cacheable {
		t.Skip("requires APFS")
	}
	os.WriteFile(filepath.Join(root, "hot/nested/a.txt"), []byte("new"), 0644)
	s.recordEvents([]backup.Plan{p}, []fsevents.Event{{Path: filepath.Join(root, "hot/nested/a.txt"), ID: 2}}, false)
	preview, entries, err := s.preview(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if preview.ScanStats.SubtreeSources != 2 || preview.ScanStats.PathsVisited != 3 || preview.ScanStats.ProjectedPaths != len(full.Entries)-3 {
		t.Fatal(preview.ScanStats)
	}
	actual, err := backup.ScanIndexed(context.Background(), s.scanPlan(p), s.Store, true)
	if err != nil || !backup.EquivalentTrees(entries, actual.Entries) {
		t.Fatal("preview differs from full", err)
	}
	s.Store.DB.Exec("DELETE FROM change_events")
	preview, _, err = s.preview(context.Background(), p)
	if err != nil || preview.ScanStats.FullSources != 2 {
		t.Fatal("lost queue did not force full", preview.ScanStats, err)
	}
}
func TestMountFlagsInvalidateSubtreeHints(t *testing.T) {
	for _, flag := range []uint32{fsevents.Mount, fsevents.Unmount} {
		if !(fsevents.Event{Flags: flag}).NeedsRescan() {
			t.Fatal("mount must invalidate descendants")
		}
	}
}
func TestNativeObservationDoesNotWalkPollingSignature(t *testing.T) {
	s, p := nativeHintsFixture(t)
	// Idle native scheduling must preserve the cached observation without
	// enumerating sources. Polling retains its filesystem signature.
	p.Sources[0].Path = filepath.Join(t.TempDir(), "absent")
	cp := checkpoint{Signature: "old", ChangeSignature: "native:0", LastAudit: time.Now(), LastChangeCheck: time.Now(), Changes: &ChangeSummary{}, ChangedAt: time.Now().Add(-time.Hour)}
	if _, err := s.evaluateChanges(context.Background(), p, &cp, time.Now(), "", false); err != nil {
		t.Fatalf("native idle observation walked source: %v", err)
	}
}
