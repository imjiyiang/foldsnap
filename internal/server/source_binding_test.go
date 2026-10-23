package server

import (
	"encoding/json"
	"localbackup/internal/backup"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSourceBindingAPIKeepsIDAndHistoryOnRebind(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	if r := apiRun(t, s, p, RunRequest{}); r.Status != "success" {
		t.Fatal(r)
	}
	src := p.Sources[0]
	renamed := src.Path + "-new-name"
	if err := os.Rename(src.Path, renamed); err != nil {
		t.Fatal(err)
	}
	p.Sources[0].Path = renamed
	w := request(t, s, "PATCH", "plans/"+p.ID, p)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var rebound backup.Plan
	json.Unmarshal(w.Body.Bytes(), &rebound)
	if rebound.Sources[0].ID != src.ID || rebound.Sources[0].DirectoryID != src.DirectoryID || rebound.Sources[0].VolumeID != src.VolumeID {
		t.Fatal("rebinding lost identity")
	}
	if r := apiRun(t, s, rebound, RunRequest{}); r.Status != "success" {
		t.Fatal(r)
	}
	all, _ := s.Store.Snapshots()
	if len(all) != 2 {
		t.Fatal("lost prior history")
	}
}
func TestSourceBindingCannotBeClearedOrForgedToReplaceRoot(t *testing.T) {
	for _, mode := range []string{"clear", "forge"} {
		t.Run(mode, func(t *testing.T) {
			s := testServer(t)
			p := saveFixturePlan(t, s, "mirror")
			src := p.Sources[0]
			replacement := filepath.Join(t.TempDir(), "different-root")
			os.Mkdir(replacement, 0755)
			p.Sources[0].Path = replacement
			p.Sources[0].VolumeID = ""
			p.Sources[0].DirectoryID = ""
			p.Sources[0].DirectoryBirthTime = ""
			if mode == "forge" {
				p.Sources[0], _ = backup.BindSource(p.Sources[0])
			}
			w := request(t, s, "PATCH", "plans/"+p.ID, p)
			if w.Code != 400 {
				t.Fatal("changed existing identity", w.Body.String())
			}
			var saved backup.Plan
			s.Store.Get("backup_plans", p.ID, &saved)
			if saved.Sources[0].Path != src.Path || saved.Sources[0].DirectoryID != src.DirectoryID {
				t.Fatal("replaced source on failed request")
			}
		})
	}
}
func TestMissingSourceWaitsSurvivesRestartAndResumesOriginal(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	original := p.Sources[0].Path
	os.Rename(original, original+"-disconnected")
	r := queuedRequest(t, s, p)
	waitStatus(t, s, r.ID, "waiting_for_source")
	if snaps, _ := s.Store.Snapshots(); len(snaps) != 0 {
		t.Fatal("published incomplete sources")
	}
	restarted := New(s.Store, s.Token, s.Host, s.Web, false)
	r = waitStatus(t, restarted, r.ID, "waiting_for_source")
	runControl(t, restarted, r.ID, "pause")
	waitStatus(t, restarted, r.ID, "paused")
	os.Rename(original+"-disconnected", original)
	runControl(t, restarted, r.ID, "resume")
	r = waitRun(t, restarted, r.ID)
	if r.Status != "success" {
		t.Fatal(r)
	}
}
func TestReplacedSourceRequiresConfirmationAndCannotPublishMirror(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "mirror")
	if r := apiRun(t, s, p, RunRequest{}); r.Status != "success" {
		t.Fatal(r)
	}
	root := p.Sources[0].Path
	os.Rename(root, root+"-original")
	os.Mkdir(root, 0755)
	r := apiRun(t, s, p, RunRequest{})
	if r.Status != "needs_confirmation" || !strings.Contains(r.Error, "身份") {
		t.Fatal("replacement passed as source", r)
	}
	if snaps, _ := s.Store.Snapshots(); len(snaps) != 1 {
		t.Fatal("published replacement mirror")
	}
}
func TestLegacySourceRequiresExplicitSaveBeforeBinding(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	for i := range p.Sources {
		p.Sources[i].VolumeID = ""
		p.Sources[i].DirectoryID = ""
		p.Sources[i].DirectoryBirthTime = ""
		p.Sources[i].MountPath = ""
		p.Sources[i].VolumeRelativePath = ""
	}
	s.Store.Put("backup_plans", p.ID, p)
	if w := request(t, s, "POST", "plans/"+p.ID+"/preview", map[string]any{}); w.Code != 400 || !strings.Contains(w.Body.String(), "绑定") {
		t.Fatal("silently accepted legacy root", w.Body.String())
	}
	w := request(t, s, "PATCH", "plans/"+p.ID, p)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var saved backup.Plan
	json.Unmarshal(w.Body.Bytes(), &saved)
	for _, src := range saved.Sources {
		if !backup.SourceBound(src) {
			t.Fatal("save failed to bind legacy source")
		}
	}
	if r := apiRun(t, s, saved, RunRequest{}); r.Status != "success" {
		t.Fatal(r)
	}
}
func TestSourceInspectionIsAuthenticatedReadOnlyAndReportsMismatch(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	src := p.Sources[0]
	w := request(t, s, "POST", "system/source/inspect", src)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"bound"`) {
		t.Fatal(w.Body.String())
	}
	src.Path = p.Sources[1].Path
	w = request(t, s, "POST", "system/source/inspect", src)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"changed"`) {
		t.Fatal(w.Body.String())
	}
	var after backup.Plan
	s.Store.Get("backup_plans", p.ID, &after)
	if after.Sources[0].Path != p.Sources[0].Path {
		t.Fatal("inspection changed binding")
	}
}

func TestSourceRebindResetsOldFailureBackoffForFreshAudit(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	s.Store.Put("scan_checkpoints", p.ID, checkpoint{Failures: 3, Signature: "old-root"})
	renamed := p.Sources[0].Path + "-rename"
	os.Rename(p.Sources[0].Path, renamed)
	p.Sources[0].Path = renamed
	w := request(t, s, "PATCH", "plans/"+p.ID, p)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var cp checkpoint
	s.Store.Get("scan_checkpoints", p.ID, &cp)
	if cp.Failures != 0 || cp.Signature != "" || !cp.LastAudit.IsZero() {
		t.Fatal("corrected binding remained locked behind old failure state", cp)
	}
}

func TestSourceWaitingPersistenceRollsBackBothRecordsOnError(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	s.mu.Lock()
	s.active = "maintenance"
	s.mu.Unlock()
	r := queuedRequest(t, s, p)
	r.Status = "waiting_for_source"
	r.Stage = "waiting"
	_, err := s.Store.DB.Exec("CREATE TRIGGER reject_wait BEFORE UPDATE ON run_jobs BEGIN SELECT RAISE(ABORT,'simulated interrupted wait persistence'); END")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.persistWaiting(s.ctx, r); err == nil {
		t.Fatal("fault injection did not abort")
	}
	var after backup.Run
	s.Store.Get("backup_runs", r.ID, &after)
	if after.Status != "queued" {
		t.Fatal("partially persisted waiting run")
	}
	s.Store.DB.Exec("DROP TRIGGER reject_wait")
	if err = s.persistWaiting(s.ctx, r); err != nil {
		t.Fatal(err)
	}
	restarted := New(s.Store, s.Token, s.Host, s.Web, false)
	waitStatus(t, restarted, r.ID, "waiting_for_source")
	runControl(t, restarted, r.ID, "cancel")
}
func TestRelocatedSourceCannotEnterAnotherPlanRepository(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	other := saveFixturePlan(t, s, "snapshot")
	moved := filepath.Join(other.Destinations[0].Path, "moved-source")
	os.Rename(p.Sources[0].Path, moved)
	p.Sources[0].Path = moved
	if _, err := s.resolveSources(p); err == nil {
		t.Fatal("source entered another plan's backup target")
	}
}
