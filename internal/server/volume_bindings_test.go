package server

import (
	"encoding/json"
	"errors"
	"localbackup/internal/backup"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestMountTransitionsTrackRotationAndPendingInsertion(t *testing.T) {
	p := backup.Plan{Schedule: backup.Schedule{OnMount: true}, Destinations: []backup.Destination{{ID: "a", Enabled: true}, {ID: "b", Enabled: true}}}
	online := map[string]bool{"a": true}
	check := func(d backup.Destination) (string, error) {
		if online[d.ID] {
			return d.ID, nil
		}
		return "", errors.New("offline or wrong UUID")
	}
	var cp checkpoint
	observeMounts(&cp, p, check)
	if cp.Online || !reflect.DeepEqual(pendingMountIDs(cp, p), []string{"a"}) {
		t.Fatalf("rotation requires only connected A: %+v", cp)
	}
	// Repeated discovery while the queue is busy must retain, not duplicate, A.
	observeMounts(&cp, p, check)
	if len(cp.PendingMounts) != 1 {
		t.Fatal(cp)
	}
	delete(cp.PendingMounts, "a")
	observeMounts(&cp, p, check)
	if len(cp.PendingMounts) != 0 {
		t.Fatal("steady online retriggered")
	}
	online["b"] = true
	observeMounts(&cp, p, check)
	if !reflect.DeepEqual(pendingMountIDs(cp, p), []string{"b"}) {
		t.Fatal(cp)
	}
	// Unplug before dispatch cancels the pending insertion. Replug adds it again.
	online["b"] = false
	observeMounts(&cp, p, check)
	if len(cp.PendingMounts) != 0 {
		t.Fatal(cp)
	}
	online["a"] = false
	online["b"] = true
	observeMounts(&cp, p, check)
	if !reflect.DeepEqual(pendingMountIDs(cp, p), []string{"b"}) {
		t.Fatal(cp)
	}
	p.Schedule.OnMount = false
	observeMounts(&cp, p, check)
	if len(cp.PendingMounts) != 0 {
		t.Fatal("disabled onMount retained a trigger")
	}
}
func TestScopedMountRejectsInvalidTargetsAndManualBypass(t *testing.T) {
	p := backup.Plan{Enabled: true, Schedule: backup.Schedule{OnMount: true}, Destinations: []backup.Destination{{ID: "a", Enabled: true}, {ID: "b", Enabled: false}}}
	for _, r := range []backup.Run{{Trigger: "manual", DestinationIDs: []string{"a"}}, {Trigger: "volume", DestinationIDs: []string{"b"}}, {Trigger: "volume", DestinationIDs: []string{"unknown"}}, {Trigger: "volume", DestinationIDs: []string{"a", "a"}}, {Trigger: "volume", DestinationIDs: []string{"a"}, DeletesConfirmed: true}} {
		if _, err := scopedMountPlan(p, r); err == nil {
			t.Fatalf("accepted %+v", r)
		}
	}
	scoped, err := scopedMountPlan(p, backup.Run{Trigger: "volume", DestinationIDs: []string{"a"}})
	if err != nil || len(scoped.Destinations) != 1 || len(p.Destinations) != 2 {
		t.Fatalf("%+v %v", scoped, err)
	}
	var req RunRequest
	json.Unmarshal([]byte(`{"destinationIds":["a"]}`), &req)
	if len(req.DestinationIDs) > 0 {
		t.Fatal("HTTP allowed scope injection")
	}
}
func TestVolumeBindingsRemainVisibleOfflineAndNeverMatchLabels(t *testing.T) {
	p := backup.Plan{ID: "plan-a", Name: "Work", Enabled: true, Schedule: backup.Schedule{OnMount: true}, Destinations: []backup.Destination{{ID: "a", VolumeID: "uuid-a", Name: "Same name", Enabled: true, MountPath: "/Volumes/Old name", VolumeRelativePath: "backup"}}}
	out := mergeVolumeBindings([]map[string]any{{"id": "uuid-b", "name": "Same name", "connected": true}}, []backup.Plan{p})
	if len(out) != 2 || out[1]["connected"] != false || out[1]["id"] != "uuid-a" {
		t.Fatal(out)
	}
	bindings := out[1]["bindings"].([]map[string]any)
	if len(bindings) != 1 || bindings[0]["automatic"] != true {
		t.Fatal(bindings)
	}
	out = mergeVolumeBindings([]map[string]any{{"id": "uuid-a", "name": "Renamed disk", "path": "/Volumes/Renamed disk", "connected": true}}, []backup.Plan{p})
	if len(out) != 1 || out[0]["path"] != "/Volumes/Renamed disk" {
		t.Fatal(out)
	}
}
func TestMountScopedQueueExecutesOneDiskAndRetainsFullPlan(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	p.Schedule = backup.Schedule{OnMount: true}
	second := backup.Destination{ID: backup.ID("dst-"), Name: "Offline rotation disk", Path: filepath.Join(t.TempDir(), "missing"), VolumeID: "absent-volume", Enabled: true}
	p.Destinations = append(p.Destinations, second)
	if err := s.Store.Put("backup_plans", p.ID, p); err != nil {
		t.Fatal(err)
	}
	// Freeze dispatch to prove the complete plan and selected target survive in SQLite.
	s.mu.Lock()
	s.active = "maintenance"
	s.mu.Unlock()
	r, err := s.startWithRequest(p, "volume", RunRequest{DestinationIDs: []string{p.Destinations[0].ID}})
	if err != nil {
		t.Fatal(err)
	}
	var data string
	if err = s.Store.DB.QueryRow("SELECT plan_data FROM run_jobs WHERE run_id=?", r.ID).Scan(&data); err != nil {
		t.Fatal(err)
	}
	var queued backup.Plan
	json.Unmarshal([]byte(data), &queued)
	if len(queued.Destinations) != 2 || len(r.DestinationIDs) != 1 {
		t.Fatal("lost durable full plan or scope")
	}
	s.mu.Lock()
	s.active = ""
	s.dispatchLocked()
	s.mu.Unlock()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if err = s.Store.Get("backup_runs", r.ID, &r); err != nil {
			t.Fatal(err)
		}
		if r.FinishedAt != nil {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if r.Status != "success" {
		t.Fatalf("rotation run: %+v", r)
	}
	snaps, _ := s.Store.Snapshots()
	if len(snaps) != 1 || snaps[0].DestinationID != p.Destinations[0].ID {
		t.Fatal(snaps)
	}
	if _, err = os.Stat(second.Path); !os.IsNotExist(err) {
		t.Fatal("created absent disk path")
	}
	var saved backup.Plan
	s.Store.Get("backup_plans", p.ID, &saved)
	if len(saved.Destinations) != 2 || !saved.Destinations[1].Enabled {
		t.Fatal("mutated saved plan")
	}
	// A real subsequent polling tick must not create duplicate insertion jobs.
	p.Enabled = false
	s.Store.Put("backup_plans", p.ID, p)
}

func TestOfflineDiskAllowsScheduleChangesWithoutRebinding(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	// The first registration is validated online; later absence must retain UUID.
	path := p.Destinations[0].Path
	if err := os.Rename(path, path+"-unplugged"); err != nil {
		t.Fatal(err)
	}
	p.Schedule.OnMount = true
	w := request(t, s, "PATCH", "plans/"+p.ID, p)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var saved backup.Plan
	json.Unmarshal(w.Body.Bytes(), &saved)
	if saved.Destinations[0].VolumeID != p.Destinations[0].VolumeID {
		t.Fatal("rebound offline disk")
	}
	p.Schedule.IntervalMinutes = -1
	if w = request(t, s, "PATCH", "plans/"+p.ID, p); w.Code != 400 {
		t.Fatal(w.Body.String())
	}
	// Forged UUID/path edits still require online validation.
	p.Schedule.IntervalMinutes = 0
	p.Destinations[0].VolumeID = "forged"
	if w = request(t, s, "PATCH", "plans/"+p.ID, p); w.Code != 400 {
		t.Fatal(w.Body.String())
	}
}

func TestSchedulerKeepsSecondDiskInsertionWhileFirstJobQueued(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	p.Schedule = backup.Schedule{OnMount: true}
	b := backup.Destination{ID: backup.ID("dst-"), Name: "B", Path: filepath.Join(t.TempDir(), "b"), Enabled: true}
	os.Mkdir(b.Path, 0755)
	b.Path, _ = backup.Canonical(b.Path)
	b.VolumeID, _ = backup.VolumeID(b.Path)
	p.Destinations = append(p.Destinations, b)
	s.Store.Put("backup_plans", p.ID, p)
	os.Rename(b.Path, b.Path+"-offline")
	s.mu.Lock()
	s.active = "maintenance"
	s.mu.Unlock()
	s.tick(s.ctx)
	runs, _ := s.queueRuns()
	if len(runs) != 1 || !reflect.DeepEqual(runs[0].DestinationIDs, []string{p.Destinations[0].ID}) {
		t.Fatal(runs)
	}
	os.Rename(b.Path+"-offline", b.Path)
	s.tick(s.ctx)
	var cp checkpoint
	s.Store.Get("scan_checkpoints", p.ID, &cp)
	if !cp.PendingMounts[b.ID] {
		t.Fatal("insertion during pending run was dropped")
	}
	s.controlRun(runs[0].ID, "cancel")
	s.tick(s.ctx)
	runs, _ = s.queueRuns()
	if len(runs) != 1 || !reflect.DeepEqual(runs[0].DestinationIDs, []string{b.ID}) {
		t.Fatalf("B should follow A: %+v", runs)
	}
	s.tick(s.ctx)
	if q, _ := s.queueRuns(); len(q) != 1 {
		t.Fatal("duplicate insertion jobs")
	}
	s.controlRun(runs[0].ID, "cancel")
	p.Enabled = false
	s.Store.Put("backup_plans", p.ID, p)
}
