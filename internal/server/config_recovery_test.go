package server

import (
	"encoding/json"
	"localbackup/internal/backup"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func recoveryFixture(t *testing.T) (*Server, backup.Plan, backup.Snapshot) {
	t.Helper()
	s := testServer(t)
	p := planFixture(t)
	p.Enabled = true
	p.Schedule = backup.Schedule{OnMount: true, IntervalMinutes: 60}
	p.Rules = []backup.Rule{{Action: "exclude", Pattern: "*.tmp"}}
	p.Retention = backup.Retention{KeepLast: 7, KeepDays: 30, KeepDaily: 7, KeepWeekly: 4, KeepMonthly: 12}
	w := request(t, s, "POST", "plans", p)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &p)
	r, err := s.startWithRequest(p, "manual", RunRequest{SourceIDs: []string{p.Sources[0].ID}})
	if err != nil {
		t.Fatal(err)
	}
	r = waitRun(t, s, r.ID)
	if r.Status != "success" {
		t.Fatal(r)
	}
	for s.busy() {
		time.Sleep(time.Millisecond)
	}
	sn := s.latest(p.ID, p.Destinations[0].ID)
	if sn == nil || sn.PlanConfig == nil || len(sn.PlanConfig.Sources) != 2 || !sn.PlanConfig.Sources[1].Enabled {
		t.Fatal("scoped backup lost full configuration", sn)
	}
	w = request(t, s, "PATCH", "snapshots/"+sn.ID, map[string]bool{"pinned": true})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	return s, p, *sn
}
func recoveryPreview(t *testing.T, s *Server, p backup.Plan) recoveryView {
	t.Helper()
	w := request(t, s, "POST", "repositories/recovery/preview", map[string]string{"path": p.Destinations[0].Path})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var v recoveryView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}
func TestConfigurationRecoveryIntoEmptyDatabaseIsPausedAndAtomic(t *testing.T) {
	_, p, sn := recoveryFixture(t)
	// Source availability is not required to preserve its original physical binding.
	if err := os.Rename(p.Sources[0].Path, p.Sources[0].Path+"-offline"); err != nil {
		t.Fatal(err)
	}
	fresh := testServer(t)
	v := recoveryPreview(t, fresh, p)
	if len(v.Plans) != 1 || v.Plans[0].Enabled || v.Pinned != 1 || v.Snapshots != 1 {
		t.Fatal(v)
	}
	plans, _ := fresh.Store.Plans()
	snapshots, _ := fresh.Store.Snapshots()
	if len(plans) != 0 || len(snapshots) != 0 {
		t.Fatal("preview wrote database")
	}
	w := request(t, fresh, "POST", "repositories/recovery/apply", map[string]any{"previewId": v.PreviewID, "restorePlans": true})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	plans, _ = fresh.Store.Plans()
	if len(plans) != 1 || plans[0].Enabled || plans[0].Schedule.IntervalMinutes != 60 || !plans[0].Schedule.OnMount || plans[0].Retention != p.Retention || plans[0].Sources[0].VolumeID != p.Sources[0].VolumeID || len(plans[0].Rules) != 1 {
		t.Fatal(plans)
	}
	snapshots, _ = fresh.Store.Snapshots()
	if len(snapshots) != 1 || !snapshots[0].Pinned {
		t.Fatal(snapshots)
	}
	var count int
	fresh.Store.DB.QueryRow("SELECT COUNT(*) FROM file_entries").Scan(&count)
	if count != len(sn.Entries) {
		t.Fatal("historical index missing", count)
	}
	fresh.Store.DB.QueryRow("SELECT COUNT(*) FROM source_scans").Scan(&count)
	if count != 0 {
		t.Fatal("recovery created trusted live cache")
	}
	runs, _ := fresh.Store.Runs()
	if len(runs) != 0 || fresh.busy() {
		t.Fatal("recovery executed a plan")
	}
	if err := os.Rename(p.Sources[0].Path+"-offline", p.Sources[0].Path); err != nil {
		t.Fatal(err)
	}
	scan := request(t, fresh, "POST", "system/index/scan", map[string]string{"planId": p.ID})
	if scan.Code != 200 {
		t.Fatal(scan.Body.String())
	}
	fresh.Store.DB.QueryRow("SELECT COUNT(*) FROM source_scans").Scan(&count)
	if count != 2 {
		t.Fatal("fresh live index not rebuilt from actual sources", count)
	}
	if w = request(t, fresh, "POST", "repositories/recovery/apply", map[string]any{"previewId": v.PreviewID, "restorePlans": true}); w.Code != 409 {
		t.Fatal("preview replay accepted")
	}
}
func TestRecoveryNeverOverwritesExistingPlanAndSupportsHistoryOnly(t *testing.T) {
	s, p, _ := recoveryFixture(t)
	p.Name = "Local changes"
	p.Enabled = false
	s.Store.Put("backup_plans", p.ID, p)
	v := recoveryPreview(t, s, p)
	if len(v.Plans) != 0 || len(v.Warnings) == 0 {
		t.Fatal(v)
	}
	w := request(t, s, "POST", "repositories/recovery/apply", map[string]any{"previewId": v.PreviewID, "restorePlans": true})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var old backup.Plan
	s.Store.Get("backup_plans", p.ID, &old)
	if old.Name != "Local changes" {
		t.Fatal("plan overwritten")
	}
	fresh := testServer(t)
	v = recoveryPreview(t, fresh, p)
	w = request(t, fresh, "POST", "repositories/recovery/apply", map[string]any{"previewId": v.PreviewID, "restorePlans": false})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	plans, _ := fresh.Store.Plans()
	if len(plans) != 0 {
		t.Fatal("history-only imported plan")
	}
}
func TestRecoveryStaleLocalOrDiskStateCannotCommit(t *testing.T) {
	for _, kind := range []string{"local", "pin", "content", "expired"} {
		t.Run(kind, func(t *testing.T) {
			_, p, sn := recoveryFixture(t)
			fresh := testServer(t)
			v := recoveryPreview(t, fresh, p)
			switch kind {
			case "local":
				fresh.Store.Put("backup_plans", p.ID, p)
			case "pin":
				if err := backup.SaveSnapshotPin(sn, false); err != nil {
					t.Fatal(err)
				}
			case "content":
				if err := os.WriteFile(filepath.Join(sn.Path, sn.Entries[0].SourceID, "one.txt"), []byte("corrupt"), 0644); err != nil {
					t.Fatal(err)
				}
			case "expired":
				g := fresh.recoveries[v.PreviewID]
				g.View.ExpiresAt = time.Now().Add(-time.Minute)
				fresh.recoveries[v.PreviewID] = g
			}
			w := request(t, fresh, "POST", "repositories/recovery/apply", map[string]any{"previewId": v.PreviewID, "restorePlans": true})
			if w.Code == 200 {
				t.Fatal("stale preview committed", kind)
			}
			snapshots, _ := fresh.Store.Snapshots()
			if len(snapshots) != 0 {
				t.Fatal("partial import survived rejected apply")
			}
		})
	}
}

func TestRecoveryTransactionFailureRollsBackPlansAndHistory(t *testing.T) {
	_, p, _ := recoveryFixture(t)
	fresh := testServer(t)
	v := recoveryPreview(t, fresh, p)
	if _, err := fresh.Store.DB.Exec("CREATE TRIGGER reject_recovery BEFORE INSERT ON snapshots BEGIN SELECT RAISE(ABORT,'simulated disk full'); END"); err != nil {
		t.Fatal(err)
	}
	w := request(t, fresh, "POST", "repositories/recovery/apply", map[string]any{"previewId": v.PreviewID, "restorePlans": true})
	if w.Code == 200 {
		t.Fatal("failed transaction succeeded")
	}
	plans, _ := fresh.Store.Plans()
	snapshots, _ := fresh.Store.Snapshots()
	if len(plans) != 0 || len(snapshots) != 0 {
		t.Fatal("partial recovery escaped rollback")
	}
}
func TestUnknownOrCorruptDiskPinNeverLosesProtection(t *testing.T) {
	for _, kind := range []string{"missing", "corrupt", "link"} {
		t.Run(kind, func(t *testing.T) {
			_, p, sn := recoveryFixture(t)
			path := filepath.Join(p.Destinations[0].Path, "LocalBackup", "metadata", "pins", sn.ID+".json")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if kind == "corrupt" {
				os.WriteFile(path, []byte(`{"pinned":false}`), 0600)
			}
			if kind == "link" {
				os.Symlink(filepath.Join(sn.Path, "manifest.json"), path)
			}
			fresh := testServer(t)
			v := recoveryPreview(t, fresh, p)
			if v.Pinned != 1 || len(v.Warnings) == 0 {
				t.Fatal("pin uncertainty not surfaced", v)
			}
			w := request(t, fresh, "POST", "repositories/recovery/apply", map[string]any{"previewId": v.PreviewID, "restorePlans": true})
			if w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			snapshots, _ := fresh.Store.Snapshots()
			if !snapshots[0].Pinned {
				t.Fatal("uncertain snapshot lost protection")
			}
		})
	}
}
