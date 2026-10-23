package server

import (
	"context"
	"encoding/json"
	"localbackup/internal/backup"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func scopedPreview(t *testing.T, s *Server, p backup.Plan, ids []string) backup.Preview {
	t.Helper()
	w := request(t, s, "POST", "plans/"+p.ID+"/preview", map[string]any{"sourceIds": ids})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var v backup.Preview
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}
func sourceContent(t *testing.T, snap backup.Snapshot, id, path string) string {
	t.Helper()
	root, err := backup.ValidateSnapshot(snap)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, id, path))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
func TestSourceScopePreservesOtherSourcesAcrossAllModes(t *testing.T) {
	for _, mode := range []string{"snapshot", "incremental", "mirror"} {
		t.Run(mode, func(t *testing.T) {
			s := testServer(t)
			p := saveFixturePlan(t, s, mode)
			if run := apiRun(t, s, p, RunRequest{}); run.Status != "success" {
				t.Fatalf("baseline %+v", run)
			}
			original := s.latest(p.ID, p.Destinations[0].ID)
			a, b := p.Sources[0], p.Sources[1]
			if err := os.WriteFile(filepath.Join(a.Path, "one.txt"), []byte("new A"), 0644); err != nil {
				t.Fatal(err)
			}
			// An unselected source may be unavailable. It is never read or removed.
			if err := os.Rename(b.Path, b.Path+"-offline"); err != nil {
				t.Fatal(err)
			}
			changed := time.Now().Add(-time.Minute).UnixNano()
			_, err := s.Store.DB.Exec("INSERT INTO change_events(plan_id,source_id,relative_path,event_id,flags,requires_scan,changed_at_ns) VALUES(?,?,?,?,?,?,?)", p.ID, b.ID, "two.txt", "42", 0, 0, changed)
			if err != nil {
				t.Fatal(err)
			}
			before, _ := s.pending(p.ID)
			preview := scopedPreview(t, s, p, []string{a.ID})
			if preview.Modified != 1 || preview.Deleted != 0 || preview.DeletePaths != 0 || !reflect.DeepEqual(preview.SourceIDs, []string{a.ID}) {
				t.Fatalf("preview %+v", preview)
			}
			for _, op := range preview.Operations {
				if op.SourceID == b.ID && op.Type != "retained" {
					t.Fatalf("changed unselected source: %+v", op)
				}
			}
			run := apiRun(t, s, p, RunRequest{SourceIDs: []string{a.ID}, PreviewID: preview.PreviewID})
			if run.Status != "success" || !reflect.DeepEqual(run.SourceNames, []string{a.Name}) {
				t.Fatalf("partial %+v", run)
			}
			latest := s.latest(p.ID, p.Destinations[0].ID)
			if sourceContent(t, *latest, a.ID, "one.txt") != "new A" || sourceContent(t, *latest, b.ID, "two.txt") != "second source" {
				t.Fatal("wrong composed content")
			}
			if err = backup.AuditSnapshot(context.Background(), *latest); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(latest.UpdatedSourceIDs, []string{a.ID}) {
				t.Fatal("snapshot lost update scope")
			}
			if sourceContent(t, *original, a.ID, "one.txt") != "first source" {
				t.Fatal("mutated immutable old snapshot")
			}
			after, _ := s.pending(p.ID)
			if before.Revision != after.Revision || after.Count == 0 {
				t.Fatal("partial run acknowledged unselected source events")
			}
			var saved backup.Plan
			s.Store.Get("backup_plans", p.ID, &saved)
			if !reflect.DeepEqual(saved, p) {
				t.Fatal("persisted temporary source disable flags")
			}
			// Scope must survive the on-disk manifest, not only the SQLite response.
			root, _ := backup.CheckDestination(p.Destinations[0])
			data, err := os.ReadFile(filepath.Join(root, "plans", p.ID, "snapshots", latest.ID, "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			var manifest backup.Snapshot
			json.Unmarshal(data, &manifest)
			if !reflect.DeepEqual(manifest.UpdatedSourceIDs, []string{a.ID}) {
				t.Fatal("manifest lost scope")
			}
		})
	}
}
func TestSourceScopeFirstBackupIncludesOnlyChosenDirectory(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	b := p.Sources[1]
	os.Rename(b.Path, b.Path+"-offline")
	v := scopedPreview(t, s, p, []string{p.Sources[0].ID})
	if v.Files != 1 {
		t.Fatal(v)
	}
	r := apiRun(t, s, p, RunRequest{SourceIDs: v.SourceIDs, PreviewID: v.PreviewID})
	if r.Status != "success" {
		t.Fatal(r)
	}
	snap := s.latest(p.ID, p.Destinations[0].ID)
	if len(snap.Sources) != 1 || len(snap.Entries) != 1 || snap.Sources[0].ID == b.ID {
		t.Fatal("fabricated backup of unselected directory")
	}
}
func TestSourceScopeRejectsInvalidAndMismatchedPreview(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	for _, ids := range [][]string{{"foreign"}, {p.Sources[0].ID, p.Sources[0].ID}, {"../bad"}} {
		if w := request(t, s, "POST", "plans/"+p.ID+"/preview", map[string]any{"sourceIds": ids}); w.Code != 400 {
			t.Fatal(w.Body.String())
		}
		if w := request(t, s, "POST", "plans/"+p.ID+"/runs", RunRequest{SourceIDs: ids}); w.Code != 409 {
			t.Fatal(w.Body.String())
		}
	}
	v := scopedPreview(t, s, p, []string{p.Sources[0].ID})
	for _, ids := range [][]string{nil, {p.Sources[1].ID}} {
		w := request(t, s, "POST", "plans/"+p.ID+"/runs", RunRequest{SourceIDs: ids, PreviewID: v.PreviewID})
		if w.Code != 409 {
			t.Fatal("accepted approval for different scope", w.Body.String())
		}
	}
	// Rejected mismatches do not consume the original grant.
	if r := apiRun(t, s, p, RunRequest{SourceIDs: v.SourceIDs, PreviewID: v.PreviewID}); r.Status != "success" {
		t.Fatal(r)
	}
	p.Sources[0].Enabled = false
	s.Store.Put("backup_plans", p.ID, p)
	if w := request(t, s, "POST", "plans/"+p.ID+"/preview", map[string]any{"sourceIds": []string{p.Sources[0].ID}}); w.Code != 400 {
		t.Fatal(w.Body.String())
	}
}
func TestSourceScopeQueueDeduplicatesSameScopeButKeepsDifferentScopes(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	s.mu.Lock()
	s.active = "maintenance"
	s.mu.Unlock()
	enqueue := func(ids []string) backup.Run {
		w := request(t, s, "POST", "plans/"+p.ID+"/runs", RunRequest{SourceIDs: ids})
		if w.Code != 202 {
			t.Fatal(w.Body.String())
		}
		var r backup.Run
		json.Unmarshal(w.Body.Bytes(), &r)
		return r
	}
	a := enqueue([]string{p.Sources[0].ID})
	again := enqueue([]string{p.Sources[0].ID})
	b := enqueue([]string{p.Sources[1].ID})
	full := enqueue(nil)
	if a.ID != again.ID || a.ID == b.ID || full.ID == b.ID {
		t.Fatal("incorrect scope coalescing")
	}
	// An explicit selection of every active source is the same scope as full.
	all := enqueue([]string{p.Sources[1].ID, p.Sources[0].ID})
	if all.ID != full.ID {
		t.Fatal("all-enabled scope not normalized")
	}
	queued, err := s.queueRuns()
	if err != nil || len(queued) != 3 {
		t.Fatalf("%+v %v", queued, err)
	}
	for _, r := range queued {
		s.controlRun(r.ID, "cancel")
	}
}
func TestSourceScopeQueuedFullPlanChangeCannotBypassValidation(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	s.mu.Lock()
	s.active = "maintenance"
	s.mu.Unlock()
	r, err := s.startWithRequest(p, "manual", RunRequest{SourceIDs: []string{p.Sources[0].ID}})
	if err != nil {
		t.Fatal(err)
	}
	// Even changes to an unselected source invalidate the saved full configuration.
	p.Sources[1].TargetPath = "new mapping"
	s.Store.Put("backup_plans", p.ID, p)
	s.mu.Lock()
	s.active = ""
	s.dispatchLocked()
	s.mu.Unlock()
	r = waitRun(t, s, r.ID)
	if r.Status != "needs_confirmation" {
		t.Fatalf("bypassed full plan comparison: %+v", r)
	}
	if snaps, _ := s.Store.Snapshots(); len(snaps) != 0 {
		t.Fatal("published stale queued plan")
	}
}

func TestSourceScopeMirrorDeleteApprovalProtectsUnselectedDirectory(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "mirror")
	if r := apiRun(t, s, p, RunRequest{}); r.Status != "success" {
		t.Fatal(r)
	}
	a, b := p.Sources[0], p.Sources[1]
	os.Remove(filepath.Join(a.Path, "one.txt"))
	os.Remove(filepath.Join(b.Path, "two.txt"))
	v := scopedPreview(t, s, p, []string{a.ID})
	if v.DeletePaths != 1 || len(v.Removals) != 1 || v.Removals[0].SourceID != a.ID {
		t.Fatalf("wrong deletion scope: %+v", v)
	}
	w := request(t, s, "POST", "plans/"+p.ID+"/runs", RunRequest{SourceIDs: v.SourceIDs, PreviewID: v.PreviewID})
	if w.Code != 409 {
		t.Fatal("unconfirmed scoped mirror deletion allowed")
	}
	r := apiRun(t, s, p, RunRequest{SourceIDs: v.SourceIDs, PreviewID: v.PreviewID, ConfirmDeletes: true})
	if r.Status != "success" {
		t.Fatal(r)
	}
	snap := s.latest(p.ID, p.Destinations[0].ID)
	if sourceContent(t, *snap, b.ID, "two.txt") != "second source" {
		t.Fatal("removed unselected B")
	}
	root, _ := backup.ValidateSnapshot(*snap)
	if _, err := os.Stat(filepath.Join(root, a.ID, "one.txt")); !os.IsNotExist(err) {
		t.Fatal("selected deletion not applied")
	}
}
func TestSourceScopeRetainedCorruptionCannotPublishSnapshot(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	if r := apiRun(t, s, p, RunRequest{}); r.Status != "success" {
		t.Fatal(r)
	}
	old := s.latest(p.ID, p.Destinations[0].ID)
	root, _ := backup.ValidateSnapshot(*old)
	// Model already damaged backup media; source-only execution must verify the
	// retained B data even though it never reads B's live source.
	if err := os.WriteFile(filepath.Join(root, p.Sources[1].ID, "two.txt"), []byte("damaged backup"), 0644); err != nil {
		t.Fatal(err)
	}
	r := apiRun(t, s, p, RunRequest{SourceIDs: []string{p.Sources[0].ID}})
	if r.Status == "success" {
		t.Fatal("published corrupt retained bytes")
	}
	snaps, _ := s.Store.Snapshots()
	if len(snaps) != 1 {
		t.Fatal("published an invalid partial snapshot")
	}
}
func TestSourceScopeQueueSurvivesRestartAndUsesLatestComposedBaseline(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	if r := apiRun(t, s, p, RunRequest{}); r.Status != "success" {
		t.Fatal(r)
	}
	os.WriteFile(filepath.Join(p.Sources[0].Path, "one.txt"), []byte("next A"), 0644)
	os.WriteFile(filepath.Join(p.Sources[1].Path, "two.txt"), []byte("next B"), 0644)
	s.mu.Lock()
	s.active = "maintenance"
	s.mu.Unlock()
	a, err := s.startWithRequest(p, "manual", RunRequest{SourceIDs: []string{p.Sources[0].ID}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.startWithRequest(p, "manual", RunRequest{SourceIDs: []string{p.Sources[1].ID}})
	if err != nil {
		t.Fatal(err)
	}
	var data string
	s.Store.DB.QueryRow("SELECT plan_data FROM run_jobs WHERE run_id=?", a.ID).Scan(&data)
	var saved backup.Plan
	json.Unmarshal([]byte(data), &saved)
	if !saved.Sources[1].Enabled {
		t.Fatal("queue did not persist full plan")
	}
	restarted := New(s.Store, s.Token, s.Host, s.Web, false)
	restarted.mu.Lock()
	restarted.dispatchLocked()
	restarted.mu.Unlock()
	if r := waitRun(t, restarted, a.ID); r.Status != "success" {
		t.Fatal(r)
	}
	if r := waitRun(t, restarted, b.ID); r.Status != "success" {
		t.Fatal(r)
	}
	latest := restarted.latest(p.ID, p.Destinations[0].ID)
	if sourceContent(t, *latest, p.Sources[0].ID, "one.txt") != "next A" || sourceContent(t, *latest, p.Sources[1].ID, "two.txt") != "next B" {
		t.Fatal("second scoped job discarded first job's new data")
	}
}

func TestSourceScopeSnapshotDropsSelectedMissingPathOnlyAndFullRunCatchesUp(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	if r := apiRun(t, s, p, RunRequest{}); r.Status != "success" {
		t.Fatal(r)
	}
	a, b := p.Sources[0], p.Sources[1]
	os.Remove(filepath.Join(a.Path, "one.txt"))
	os.Remove(filepath.Join(b.Path, "two.txt"))
	r := apiRun(t, s, p, RunRequest{SourceIDs: []string{a.ID}})
	if r.Status != "success" {
		t.Fatal(r)
	}
	snap := s.latest(p.ID, p.Destinations[0].ID)
	if len(snap.Entries) != 1 || snap.Entries[0].SourceID != b.ID {
		t.Fatalf("snapshot scope retained selected deleted path or removed B: %+v", snap.Entries)
	}
	// A later ordinary plan run processes both sources and acknowledges their
	// events. It must not keep using a stale temporary source selection.
	os.WriteFile(filepath.Join(b.Path, "two.txt"), []byte("full-run B"), 0644)
	if r = apiRun(t, s, p, RunRequest{}); r.Status != "success" || len(r.SourceIDs) != 0 {
		t.Fatal(r)
	}
	snap = s.latest(p.ID, p.Destinations[0].ID)
	if len(snap.UpdatedSourceIDs) != 0 || sourceContent(t, *snap, b.ID, "two.txt") != "full-run B" {
		t.Fatal("full plan remained accidentally scoped")
	}
}

func TestSourceScopeDoesNotPostponeWholePlanInterval(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	p.Schedule = backup.Schedule{IntervalMinutes: 60}
	s.Store.Put("backup_plans", p.ID, p)
	full := apiRun(t, s, p, RunRequest{})
	if full.Status != "success" {
		t.Fatal(full)
	}
	old := time.Now().Add(-2 * time.Hour)
	full.FinishedAt = &old
	s.Store.Put("backup_runs", full.ID, full)
	partial := apiRun(t, s, p, RunRequest{SourceIDs: []string{p.Sources[0].ID}})
	if partial.Status != "success" {
		t.Fatal(partial)
	}
	s.mu.Lock()
	s.active = "maintenance"
	s.mu.Unlock()
	s.tick(s.ctx)
	queued, err := s.queueRuns()
	if err != nil || len(queued) != 1 || queued[0].Trigger != "schedule" || len(queued[0].SourceIDs) != 0 {
		t.Fatalf("partial success postponed full plan: %+v %v", queued, err)
	}
	s.controlRun(queued[0].ID, "cancel")
}

func TestSourceScopeRetainedDirectoryRemainsRestorable(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	if r := apiRun(t, s, p, RunRequest{}); r.Status != "success" {
		t.Fatal(r)
	}
	if r := apiRun(t, s, p, RunRequest{SourceIDs: []string{p.Sources[0].ID}}); r.Status != "success" {
		t.Fatal(r)
	}
	snap := s.latest(p.ID, p.Destinations[0].ID)
	restored, err := backup.Restore(context.Background(), *snap, backup.RestoreRequest{SnapshotID: snap.ID, SourceID: p.Sources[1].ID, Path: ".", Destination: t.TempDir()}, []backup.Plan{p}, backup.DetectRsync(""))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(restored, "two.txt"))
	if err != nil || string(data) != "second source" {
		t.Fatalf("retained source cannot restore: %q %v", data, err)
	}
}

func TestSourceScopeOnlyEnabledDirectoryStillPreservesDisabledDirectory(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	if r := apiRun(t, s, p, RunRequest{}); r.Status != "success" {
		t.Fatal(r)
	}
	p.Sources[1].Enabled = false
	s.Store.Put("backup_plans", p.ID, p)
	a := p.Sources[0]
	v := scopedPreview(t, s, p, []string{a.ID})
	if !reflect.DeepEqual(v.SourceIDs, []string{a.ID}) || v.Deleted != 0 {
		t.Fatal("discarded scope because it selected all enabled sources")
	}
	r := apiRun(t, s, p, RunRequest{SourceIDs: v.SourceIDs, PreviewID: v.PreviewID})
	if r.Status != "success" {
		t.Fatal(r)
	}
	snap := s.latest(p.ID, p.Destinations[0].ID)
	if sourceContent(t, *snap, p.Sources[1].ID, "two.txt") != "second source" {
		t.Fatal("dropped disabled source from scoped generation")
	}
}
