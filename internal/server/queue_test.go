package server

import (
	"context"
	"encoding/json"
	"localbackup/internal/backup"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func queuedRequest(t *testing.T, s *Server, p backup.Plan) backup.Run {
	t.Helper()
	w := request(t, s, "POST", "plans/"+p.ID+"/runs", RunRequest{})
	if w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	var r backup.Run
	if e := json.Unmarshal(w.Body.Bytes(), &r); e != nil {
		t.Fatal(e)
	}
	return r
}
func waitStatus(t *testing.T, s *Server, id string, status string) backup.Run {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var r backup.Run
		s.Store.Get("backup_runs", id, &r)
		if r.Status == status {
			return r
		}
		if r.FinishedAt != nil {
			t.Fatalf("expected %s, got %+v", status, r)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("status timed out: " + status)
	return backup.Run{}
}
func runControl(t *testing.T, s *Server, id, action string) backup.Run {
	t.Helper()
	w := request(t, s, "POST", "runs/"+id+"/"+action, map[string]any{})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var r backup.Run
	json.Unmarshal(w.Body.Bytes(), &r)
	return r
}
func slowTransfer(t *testing.T, s *Server, p backup.Plan) {
	t.Helper()
	rs := backup.DetectRsync("")
	if !rs.Available {
		t.Skip("requires rsync")
	}
	wrapper := filepath.Join(t.TempDir(), "rsync")
	script := "#!/bin/sh\ncase \"$1\" in --version) exec \"" + rs.Path + "\" \"$@\";; esac\nexec \"" + rs.Path + "\" --bwlimit=512 \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	s.settings.RsyncPath = wrapper
	os.WriteFile(filepath.Join(p.Sources[0].Path, "large.bin"), make([]byte, 2*1024*1024), 0644)
}
func waitTransfer(t *testing.T, s *Server, id string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s.monitor.mu.Lock()
		w := s.monitor.work
		s.monitor.mu.Unlock()
		if w.RunID == id && w.Phase == "transfer" && w.Bytes > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no real transfer progress")
}
func TestPersistentQueuePauseResumeCancelAndSerialization(t *testing.T) {
	s := testServer(t)
	first := saveFixturePlan(t, s, "snapshot")
	second := saveFixturePlan(t, s, "snapshot")
	third := saveFixturePlan(t, s, "snapshot")
	slowTransfer(t, s, first)
	r1 := queuedRequest(t, s, first)
	waitTransfer(t, s, r1.ID)
	runControl(t, s, r1.ID, "pause")
	waitStatus(t, s, r1.ID, "paused")
	r2 := queuedRequest(t, s, second)
	if r2.Status != "queued" {
		t.Fatal("second writer started during pause")
	}
	if duplicate := queuedRequest(t, s, second); duplicate.ID != r2.ID {
		t.Fatal("duplicate pending plan was not merged")
	}
	runControl(t, s, r2.ID, "pause")
	waitStatus(t, s, r2.ID, "paused")
	runControl(t, s, r2.ID, "resume")
	waitStatus(t, s, r2.ID, "queued")
	r3 := queuedRequest(t, s, third)
	cancelled := runControl(t, s, r3.ID, "cancel")
	if cancelled.Status != "cancelled" || cancelled.FinishedAt == nil {
		t.Fatal("queued cancellation not terminal")
	}
	snapshots, _ := s.Store.Snapshots()
	if len(snapshots) != 0 {
		t.Fatal("paused task published a snapshot")
	}
	runControl(t, s, r1.ID, "resume")
	if r := waitRun(t, s, r1.ID); r.Status != "success" {
		t.Fatal(r.Error)
	}
	waitStatus(t, s, r2.ID, "success")
	snapshots, _ = s.Store.Snapshots()
	if len(snapshots) != 2 {
		t.Fatal("queue published an unexpected generation", len(snapshots))
	}
	// Cancelling a stopped real rsync must release the queue and leave history intact.
	os.WriteFile(filepath.Join(first.Sources[0].Path, "large.bin"), make([]byte, 3*1024*1024), 0644)
	r4 := queuedRequest(t, s, first)
	waitTransfer(t, s, r4.ID)
	runControl(t, s, r4.ID, "pause")
	waitStatus(t, s, r4.ID, "paused")
	runControl(t, s, r4.ID, "cancel")
	waitStatus(t, s, r4.ID, "cancelled")
	deadline := time.Now().Add(time.Second)
	for s.busy() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	snapshots, _ = s.Store.Snapshots()
	if len(snapshots) != 2 {
		t.Fatal("cancelled paused backup published")
	}
}
func TestQueueSurvivesRestartAndHeldJobsStayHeld(t *testing.T) {
	s := testServer(t)
	a := saveFixturePlan(t, s, "snapshot")
	b := saveFixturePlan(t, s, "snapshot")
	s.active = "restore" // deterministic occupied writer, no process or file mutation
	ra := queuedRequest(t, s, a)
	rb := queuedRequest(t, s, b)
	runControl(t, s, rb.ID, "pause")
	restarted := New(s.Store, s.Token, s.Host, s.Web, false)
	waitStatus(t, restarted, ra.ID, "queued")
	waitStatus(t, restarted, rb.ID, "paused")
	restarted.mu.Lock()
	restarted.dispatchLocked()
	restarted.mu.Unlock()
	if r := waitRun(t, restarted, ra.ID); r.Status != "success" {
		t.Fatal(r.Error)
	}
	waitStatus(t, restarted, rb.ID, "paused")
	runControl(t, restarted, rb.ID, "resume")
	if r := waitRun(t, restarted, rb.ID); r.Status != "success" {
		t.Fatal(r.Error)
	}
}
func TestOfflineQueuedJobDoesNotBlockReadyBackup(t *testing.T) {
	s := testServer(t)
	offline := saveFixturePlan(t, s, "snapshot")
	ready := saveFixturePlan(t, s, "snapshot")
	path := offline.Destinations[0].Path
	os.Rename(path, path+"-disconnected")
	r1 := queuedRequest(t, s, offline)
	waitStatus(t, s, r1.ID, "waiting_for_volume")
	r2 := queuedRequest(t, s, ready)
	if r := waitRun(t, s, r2.ID); r.Status != "success" {
		t.Fatal(r.Error)
	}
	os.Rename(path+"-disconnected", path)
	s.Store.DB.Exec("UPDATE run_jobs SET not_before_ns=0 WHERE run_id=?", r1.ID)
	s.mu.Lock()
	s.dispatchLocked()
	s.mu.Unlock()
	if r := waitRun(t, s, r1.ID); r.Status != "success" {
		t.Fatal(r.Error)
	}
}
func TestQueuedApprovalExpiryAndConfigurationChangeRequireReview(t *testing.T) {
	for _, change := range []string{"expiry", "configuration"} {
		t.Run(change, func(t *testing.T) {
			s := testServer(t)
			p := saveFixturePlan(t, s, "snapshot")
			v := getPreview(t, s, p)
			s.active = "restore"
			w := request(t, s, "POST", "plans/"+p.ID+"/runs", RunRequest{PreviewID: v.PreviewID})
			if w.Code != 202 {
				t.Fatal(w.Body.String())
			}
			var r backup.Run
			json.Unmarshal(w.Body.Bytes(), &r)
			if change == "configuration" {
				p.Description = "changed while waiting"
				s.Store.Put("backup_plans", p.ID, p)
			} else {
				var raw string
				s.Store.DB.QueryRow("SELECT approval_data FROM run_jobs WHERE run_id=?", r.ID).Scan(&raw)
				var approved previewApproval
				json.Unmarshal([]byte(raw), &approved)
				approved.ExpiresAt = time.Now().Add(-time.Minute)
				data, _ := json.Marshal(approved)
				s.Store.DB.Exec("UPDATE run_jobs SET approval_data=? WHERE run_id=?", string(data), r.ID)
			}
			s.mu.Lock()
			s.active = ""
			s.dispatchLocked()
			s.mu.Unlock()
			waitStatus(t, s, r.ID, "needs_confirmation")
			snaps, _ := s.Store.Snapshots()
			if len(snaps) != 0 {
				t.Fatal("unreviewed queued task published")
			}
		})
	}
}
func TestInterruptedPausedWriterIsRecoverable(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	r := backup.Run{ID: backup.ID("run-"), PlanID: p.ID, Status: "paused", StartedAt: time.Now()}
	s.Store.Put("backup_runs", r.ID, r)
	data, _ := json.Marshal(p)
	s.Store.DB.Exec("INSERT INTO run_jobs(run_id,plan_data,state) VALUES(?,?,'running')", r.ID, string(data))
	restarted := New(s.Store, s.Token, s.Host, s.Web, false)
	got := waitStatus(t, restarted, r.ID, "recoverable")
	if got.FinishedAt == nil {
		t.Fatal("interrupted writer remains live")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	restarted.queueLoop(ctx)
}
func TestPausePersistenceDoesNotBlockCancellationBehindIndexWriter(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	slowTransfer(t, s, p)
	r := queuedRequest(t, s, p)
	waitTransfer(t, s, r.ID)
	tx, err := s.Store.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO settings VALUES('held-index-write','{}')"); err != nil {
		t.Fatal(err)
	}
	pauseDone := make(chan int, 1)
	go func() { pauseDone <- request(t, s, "POST", "runs/"+r.ID+"/pause", map[string]any{}).Code }()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		c := s.control
		s.mu.Unlock()
		if c != nil && c.State() == "paused" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancelDone := make(chan int, 1)
	go func() { cancelDone <- request(t, s, "POST", "runs/"+r.ID+"/cancel", map[string]any{}).Code }()
	select {
	case code := <-cancelDone:
		if code != 200 {
			t.Fatal("cancel rejected", code)
		}
	case <-time.After(2 * time.Second):
		tx.Rollback()
		t.Fatal("pause persistence held the control mutex")
	}
	tx.Rollback()
	select {
	case code := <-pauseDone:
		if code != 200 {
			t.Fatal("pause response", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pause persistence did not finish")
	}
	waitStatus(t, s, r.ID, "cancelled")
}
