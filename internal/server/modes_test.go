package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"localbackup/internal/backup"
)

func waitRun(t *testing.T, s *Server, id string) backup.Run {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var r backup.Run
		if e := s.Store.Get("backup_runs", id, &r); e != nil {
			t.Fatal(e)
		}
		if r.FinishedAt != nil {
			for s.busy() && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			return r
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("run timed out")
	return backup.Run{}
}
func apiRun(t *testing.T, s *Server, p backup.Plan, req RunRequest) backup.Run {
	t.Helper()
	w := request(t, s, "POST", "plans/"+p.ID+"/runs", req)
	if w.Code != 202 {
		t.Fatalf("run rejected: %s", w.Body.String())
	}
	var r backup.Run
	json.Unmarshal(w.Body.Bytes(), &r)
	return waitRun(t, s, r.ID)
}
func saveFixturePlan(t *testing.T, s *Server, mode string) backup.Plan {
	t.Helper()
	p := planFixture(t)
	p.Mode = mode
	w := request(t, s, "POST", "plans", p)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &p)
	return p
}
func getPreview(t *testing.T, s *Server, p backup.Plan) backup.Preview {
	t.Helper()
	w := request(t, s, "POST", "plans/"+p.ID+"/preview", map[string]any{})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var v backup.Preview
	json.Unmarshal(w.Body.Bytes(), &v)
	return v
}
func TestMirrorRequiresFreshExplicitSingleUseApproval(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "mirror")
	first := apiRun(t, s, p, RunRequest{})
	if first.Status != "success" {
		t.Fatal(first.Error)
	}
	old, _ := s.Store.Snapshots()
	original := old[0]
	os.Remove(filepath.Join(p.Sources[0].Path, "one.txt"))
	v := getPreview(t, s, p)
	if v.DeletePaths != 1 || v.DeleteBytes != 12 || v.PreviewID == "" {
		t.Fatalf("bad deletion preview %+v", v)
	}
	unapproved := apiRun(t, s, p, RunRequest{})
	if unapproved.Status != "needs_confirmation" {
		t.Fatalf("mirror deletion accepted without approval %+v", unapproved)
	}
	all, _ := s.Store.Snapshots()
	if len(all) != 1 {
		t.Fatal("unapproved run published a generation")
	}
	current, e := backup.ReadCurrent(p.Destinations[0], p.ID)
	if e != nil || current.ID != original.ID {
		t.Fatal("unapproved run changed view")
	}
	w := request(t, s, "POST", "plans/"+p.ID+"/runs", RunRequest{PreviewID: v.PreviewID})
	if w.Code != 409 {
		t.Fatal("confirmation checkbox bypassed")
	}
	os.WriteFile(filepath.Join(p.Sources[1].Path, "two.txt"), []byte("changed since preview"), 0644)
	stale := apiRun(t, s, p, RunRequest{PreviewID: v.PreviewID, ConfirmDeletes: true})
	if stale.Status != "needs_confirmation" {
		t.Fatalf("stale preview accepted %+v", stale)
	}
	all, _ = s.Store.Snapshots()
	if len(all) != 1 {
		t.Fatal("stale approval published")
	}
	v = getPreview(t, s, p)
	approved := apiRun(t, s, p, RunRequest{PreviewID: v.PreviewID, ConfirmDeletes: true})
	if approved.Status != "success" || !approved.DeletesConfirmed || approved.ApprovedPreviewID != v.PreviewID {
		t.Fatalf("approved mirror failed %+v", approved)
	}
	current, e = backup.ReadCurrent(p.Destinations[0], p.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(current.Path, p.Sources[0].ID, "one.txt")); !os.IsNotExist(e) {
		t.Fatal("mirror did not remove deleted path from current view")
	}
	if e = backup.Verify(context.Background(), original.Path, original.Entries); e != nil {
		t.Fatal("mirror damaged old snapshot", e)
	}
	w = request(t, s, "POST", "plans/"+p.ID+"/runs", RunRequest{PreviewID: v.PreviewID, ConfirmDeletes: true})
	if w.Code != 409 {
		t.Fatal("used approval replayed")
	}
	v = getPreview(t, s, p)
	var grant previewApproval
	s.Store.Get("backup_previews", v.PreviewID, &grant)
	grant.ExpiresAt = time.Now().Add(-time.Minute)
	s.Store.Put("backup_previews", grant.ID, grant)
	w = request(t, s, "POST", "plans/"+p.ID+"/runs", RunRequest{PreviewID: v.PreviewID, ConfirmDeletes: true})
	if w.Code != 409 {
		t.Fatal("expired approval accepted")
	}
}
func TestScheduledMirrorWaitsWithoutRetryLoop(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "mirror")
	if r := apiRun(t, s, p, RunRequest{}); r.Status != "success" {
		t.Fatal(r.Error)
	}
	os.Remove(filepath.Join(p.Sources[0].Path, "one.txt"))
	r, e := s.start(p, "schedule")
	if e != nil {
		t.Fatal(e)
	}
	r = waitRun(t, s, r.ID)
	if r.Status != "needs_confirmation" {
		t.Fatal("automatic mirror bypassed approval")
	}
	for i := 0; i < 3; i++ {
		s.tick(context.Background())
	}
	runs, _ := s.Store.Runs()
	if len(runs) != 2 {
		t.Fatal("scheduled confirmation generated a retry loop")
	}
}
func TestRetentionProtectsCurrentEvenWhenNotLatest(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "incremental")
	if r := apiRun(t, s, p, RunRequest{}); r.Status != "success" {
		t.Fatal(r.Error)
	}
	current, e := backup.ReadCurrent(p.Destinations[0], p.ID)
	if e != nil {
		t.Fatal(e)
	}
	p.Mode = "snapshot"
	w := request(t, s, "PATCH", "plans/"+p.ID, p)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &p)
	for i := 0; i < 2; i++ {
		if err := os.WriteFile(filepath.Join(p.Sources[0].Path, "retention-version.txt"), []byte{byte('0' + i)}, 0644); err != nil {
			t.Fatal(err)
		}
		if r := apiRun(t, s, p, RunRequest{}); r.Status != "success" {
			t.Fatal(r.Error)
		}
	}
	w = request(t, s, "POST", "retention/preview", map[string]any{"planId": p.ID})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var candidates []backup.Snapshot
	json.Unmarshal(w.Body.Bytes(), &candidates)
	if len(candidates) != 1 || candidates[0].ID == current.ID {
		t.Fatal("current view was eligible for cleanup")
	}
	w = request(t, s, "POST", "retention/apply", map[string]any{"planId": p.ID, "ids": []string{candidates[0].ID}, "confirm": true})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if e = backup.Verify(context.Background(), current.Path, current.Entries); e != nil {
		t.Fatal("cleanup damaged current generation", e)
	}
}
func TestExternallyModifiedCurrentRejectedBeforeMirror(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "mirror")
	if r := apiRun(t, s, p, RunRequest{}); r.Status != "success" {
		t.Fatal(r.Error)
	}
	sn, e := backup.ReadCurrent(p.Destinations[0], p.ID)
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(sn.Path, p.Sources[0].ID, "untracked.txt")
	os.WriteFile(path, []byte("external"), 0644)
	w := request(t, s, "POST", "plans/"+p.ID+"/preview", map[string]any{})
	if w.Code != 400 {
		t.Fatal("untracked target contents were silently dropped from preview")
	}
	if _, e = os.Stat(path); e != nil {
		t.Fatal("preview modified repository")
	}
}

func TestIncrementalPreviewAcceptsTypeReplacement(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "incremental")
	if r := apiRun(t, s, p, RunRequest{}); r.Status != "success" {
		t.Fatal(r.Error)
	}
	path := filepath.Join(p.Sources[0].Path, "one.txt")
	os.Remove(path)
	os.Mkdir(path, 0755)
	os.WriteFile(filepath.Join(path, "child.txt"), []byte("new type"), 0644)
	v := getPreview(t, s, p)
	if v.Modified == 0 {
		t.Fatal("file-directory replacement missing from preview")
	}
	r := apiRun(t, s, p, RunRequest{PreviewID: v.PreviewID})
	if r.Status != "success" {
		t.Fatal(r.Error)
	}
}
