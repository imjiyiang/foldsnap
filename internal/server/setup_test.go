package server

import (
	"encoding/json"
	"localbackup/internal/backup"
	"os"
	"testing"
)

func TestSetupFreshDatabaseReportsNoInventedPlan(t *testing.T) {
	s := testServer(t)
	w := request(t, s, "GET", "system/setup", nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var report setupReport
	json.Unmarshal(w.Body.Bytes(), &report)
	if len(report.Plans) != 0 || !report.DatabaseReady || report.CheckedAt.IsZero() {
		t.Fatal(report)
	}
}
func TestSetupReadinessIsReadOnlyAndDetectsUnavailableIdentity(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	w := request(t, s, "GET", "system/setup", nil)
	var report setupReport
	json.Unmarshal(w.Body.Bytes(), &report)
	if w.Code != 200 || len(report.Plans) != 1 || !report.Plans[0].Ready {
		t.Fatal(w.Body.String())
	}
	original := p.Sources[0].Path
	os.Rename(original, original+"-offline")
	w = request(t, s, "GET", "system/setup", nil)
	json.Unmarshal(w.Body.Bytes(), &report)
	if report.Plans[0].Ready || report.Plans[0].Sources[0].State == "ready" {
		t.Fatal("offline source passed readiness")
	}
	if runs, _ := s.Store.Runs(); len(runs) != 0 {
		t.Fatal("readiness started a backup")
	}
	if q, _ := s.queueRuns(); len(q) != 0 {
		t.Fatal("readiness queued a backup")
	}
	var saved backup.Plan
	s.Store.Get("backup_plans", p.ID, &saved)
	if saved.Sources[0].Path != original {
		t.Fatal("readiness changed source binding")
	}
}
func TestSetupUnboundAndOfflineTargetCannotBeReady(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	p.Sources[0].VolumeID = ""
	p.Sources[0].DirectoryID = ""
	s.Store.Put("backup_plans", p.ID, p)
	os.Rename(p.Destinations[0].Path, p.Destinations[0].Path+"-offline")
	w := request(t, s, "GET", "system/setup", nil)
	var report setupReport
	json.Unmarshal(w.Body.Bytes(), &report)
	if w.Code != 200 || report.Plans[0].Ready || report.Plans[0].Sources[0].State != "unbound" || report.Plans[0].Destinations[0].State != "unavailable" {
		t.Fatal(w.Body.String())
	}
}

func TestSetupHistoryWithoutPlanRemainsDiscoverable(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	if r := apiRun(t, s, p, RunRequest{}); r.Status != "success" {
		t.Fatal(r)
	}
	if err := s.Store.Delete("backup_plans", p.ID); err != nil {
		t.Fatal(err)
	}
	w := request(t, s, "GET", "system/setup", nil)
	var report setupReport
	json.Unmarshal(w.Body.Bytes(), &report)
	if w.Code != 200 || len(report.Plans) != 0 || report.Snapshots != 1 {
		t.Fatal("history disappeared from onboarding", w.Body.String())
	}
}
func TestSetupReadOnlyTargetFailsPrecheck(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses DAC")
	}
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	path := p.Destinations[0].Path
	if err := os.Chmod(path, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(path, 0755)
	w := request(t, s, "GET", "system/setup", nil)
	var report setupReport
	json.Unmarshal(w.Body.Bytes(), &report)
	if w.Code != 200 || report.Plans[0].Ready || report.Plans[0].Destinations[0].State != "unwritable" {
		t.Fatal(w.Body.String())
	}
}

func TestSetupDirectoryProbeDoesNotFollowSymlink(t *testing.T) {
	root := t.TempDir()
	link := root + "-link"
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(link)
	if err := probeDirectory(link); err == nil {
		t.Fatal("directory preflight followed a source root link")
	}
	if err := probeDirectory(root); err != nil {
		t.Fatal("empty readable directory failed", err)
	}
}
