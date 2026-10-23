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

func intRef(n int) *int { return &n }
func occupyScheduler(t *testing.T, s *Server) {
	t.Helper()
	s.mu.Lock()
	s.active = "maintenance"
	s.mu.Unlock()
}
func pendingPolicy(t *testing.T, s *Server, p backup.Plan) checkpoint {
	t.Helper()
	var cp checkpoint
	if err := s.Store.Get("scan_checkpoints", p.ID, &cp); err != nil {
		t.Fatal(err)
	}
	return cp
}
func ageStableChange(t *testing.T, s *Server, p backup.Plan) {
	t.Helper()
	cp := pendingPolicy(t, s, p)
	cp.ChangedAt = time.Now().Add(-5 * time.Minute)
	if err := s.Store.Put("scan_checkpoints", p.ID, cp); err != nil {
		t.Fatal(err)
	}
}
func TestLogicalChangeSummaryDeduplicatesTargetsAndCountsRemovedBytes(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	if r := apiRun(t, s, p, RunRequest{}); r.Status != "success" {
		t.Fatal(r)
	}
	// Mirror a second complete target's baseline in the store; summary is read-only
	// and does not require writing a second repository for identical history.
	d := p.Destinations[0]
	d.ID = backup.ID("dst-")
	p.Destinations = append(p.Destinations, d)
	prev := s.latest(p.ID, p.Destinations[0].ID)
	copy := *prev
	copy.ID = backup.ID("snap-")
	copy.DestinationID = d.ID
	s.Store.Put("snapshots", copy.ID, copy)
	os.WriteFile(filepath.Join(p.Sources[0].Path, "one.txt"), []byte("A"), 0644)
	os.Remove(filepath.Join(p.Sources[1].Path, "two.txt"))
	summary, err := s.changeSummary(context.Background(), p, true)
	if err != nil || !summary.Changed || summary.Files != 2 || summary.Bytes != 25 {
		t.Fatalf("wrong union/logical sizes: %+v %v", summary, err)
	}
}
func TestChangeThresholdsAccumulateAndDoNotRehashIdleEveryTick(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	if r := apiRun(t, s, p, RunRequest{}); r.Status != "success" {
		t.Fatal(r)
	}
	p.Schedule = backup.Schedule{OnChange: true, DebounceSeconds: 10, MinChangedFiles: 2, MinChangedBytes: 25, MinIntervalMinutes: intRef(0)}
	s.Store.Put("backup_plans", p.ID, p)
	occupyScheduler(t, s)
	s.tick(s.ctx)
	os.WriteFile(filepath.Join(p.Sources[0].Path, "one.txt"), []byte("changed-one"), 0644)
	s.tick(s.ctx)
	ageStableChange(t, s, p)
	s.tick(s.ctx)
	cp := pendingPolicy(t, s, p)
	if cp.Changes == nil || cp.Changes.Files != 1 || cp.Changes.Bytes != 12 {
		t.Fatal(cp)
	}
	if q, _ := s.queueRuns(); len(q) != 0 {
		t.Fatal("below-threshold change enqueued")
	}
	checked := cp.LastChangeCheck
	s.tick(s.ctx)
	if after := pendingPolicy(t, s, p); !after.LastChangeCheck.Equal(checked) {
		t.Fatal("unchanged below-threshold poll rehashed sources")
	}
	os.WriteFile(filepath.Join(p.Sources[1].Path, "two.txt"), []byte("changed-two"), 0644)
	s.tick(s.ctx)
	ageStableChange(t, s, p)
	s.tick(s.ctx)
	q, _ := s.queueRuns()
	if len(q) != 1 || q[0].Trigger != "change" || q[0].AutomaticAttempt != 1 {
		t.Fatalf("threshold not triggered: %+v", q)
	}
	s.controlRun(q[0].ID, "cancel")
}
func TestChangeMinimumIntervalDefersUntilDue(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	baseline := apiRun(t, s, p, RunRequest{})
	if baseline.Status != "success" {
		t.Fatal(baseline)
	}
	p.Schedule = backup.Schedule{OnChange: true, DebounceSeconds: 10, MinIntervalMinutes: intRef(60)}
	s.Store.Put("backup_plans", p.ID, p)
	occupyScheduler(t, s)
	s.tick(s.ctx)
	os.WriteFile(filepath.Join(p.Sources[0].Path, "one.txt"), []byte("new bytes"), 0644)
	s.tick(s.ctx)
	ageStableChange(t, s, p)
	s.tick(s.ctx)
	if q, _ := s.queueRuns(); len(q) != 0 {
		t.Fatal("ignored min interval")
	}
	old := time.Now().Add(-2 * time.Hour)
	baseline.FinishedAt = &old
	s.Store.Put("backup_runs", baseline.ID, baseline)
	s.tick(s.ctx)
	q, _ := s.queueRuns()
	if len(q) != 1 || q[0].Trigger != "change" {
		t.Fatal(q)
	}
	s.controlRun(q[0].ID, "cancel")
}
func TestMaximumIntervalForcesFullCheckAndBypassesThresholds(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	baseline := apiRun(t, s, p, RunRequest{})
	if baseline.Status != "success" {
		t.Fatal(baseline)
	}
	p.Schedule = backup.Schedule{OnChange: false, MaxIntervalMinutes: 60, MinChangedFiles: 10000, MinChangedBytes: 1 << 30}
	s.Store.Put("backup_plans", p.ID, p)
	occupyScheduler(t, s)
	old := time.Now().Add(-2 * time.Hour)
	baseline.FinishedAt = &old
	s.Store.Put("backup_runs", baseline.ID, baseline)
	os.WriteFile(filepath.Join(p.Sources[0].Path, "one.txt"), []byte("changed"), 0644)
	s.tick(s.ctx)
	q, _ := s.queueRuns()
	if len(q) != 1 || q[0].Trigger != "max_interval" {
		t.Fatalf("deadline missed: %+v", q)
	}
	cp := pendingPolicy(t, s, p)
	if cp.LastMaxCheck.IsZero() || cp.LastAudit.IsZero() || cp.Changes.Files != 1 {
		t.Fatal(cp)
	}
	s.controlRun(q[0].ID, "cancel")
}
func TestMaximumIntervalUnchangedCheckDoesNotPublishOrRepeatScan(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	r := apiRun(t, s, p, RunRequest{})
	if r.Status != "success" {
		t.Fatal(r)
	}
	old := time.Now().Add(-2 * time.Hour)
	r.FinishedAt = &old
	s.Store.Put("backup_runs", r.ID, r)
	p.Schedule = backup.Schedule{MaxIntervalMinutes: 60}
	s.Store.Put("backup_plans", p.ID, p)
	occupyScheduler(t, s)
	s.tick(s.ctx)
	cp := pendingPolicy(t, s, p)
	if cp.LastMaxCheck.IsZero() || cp.Changes == nil || cp.Changes.Changed {
		t.Fatal(cp)
	}
	s.tick(s.ctx)
	after := pendingPolicy(t, s, p)
	if !after.LastMaxCheck.Equal(cp.LastMaxCheck) || !after.LastChangeCheck.Equal(cp.LastChangeCheck) {
		t.Fatal("repeated unchanged maximum audit")
	}
	if q, _ := s.queueRuns(); len(q) != 0 {
		t.Fatal("unchanged check created snapshot task")
	}
}
func TestBelowThresholdDirtyEventsRemainForLaterAccumulation(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	if r := apiRun(t, s, p, RunRequest{}); r.Status != "success" {
		t.Fatal(r)
	}
	p.Schedule = backup.Schedule{OnChange: true, DebounceSeconds: 10, MinChangedFiles: 2, MinIntervalMinutes: intRef(0)}
	s.Store.Put("backup_plans", p.ID, p)
	occupyScheduler(t, s)
	os.WriteFile(filepath.Join(p.Sources[0].Path, "one.txt"), []byte("changed"), 0644)
	s.Store.DB.Exec("INSERT INTO change_events(plan_id,source_id,relative_path,event_id,flags,requires_scan,changed_at_ns) VALUES(?,?,?,?,?,?,?)", p.ID, p.Sources[0].ID, "one.txt", "7", 0, 0, time.Now().Add(-time.Minute).UnixNano())
	s.watchMu.Lock()
	s.watcher = WatchStatus{Backend: "fsevents", Healthy: true}
	s.watchMu.Unlock()
	s.tick(s.ctx)
	before, _ := s.pending(p.ID)
	if before.Count != 1 {
		t.Fatal("consumed below-threshold event")
	}
	checked := pendingPolicy(t, s, p).LastChangeCheck
	s.tick(s.ctx)
	if !pendingPolicy(t, s, p).LastChangeCheck.Equal(checked) {
		t.Fatal("same event revision rehashed repeatedly")
	}
}
func TestScheduleValidationCannotBypassViaOfflineFastSave(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	os.Rename(p.Destinations[0].Path, p.Destinations[0].Path+"-offline")
	invalid := []backup.Schedule{{MinChangedFiles: -1}, {MinChangedBytes: -1}, {MinIntervalMinutes: intRef(-1)}, {MaxIntervalMinutes: 10, MinIntervalMinutes: intRef(20)}, {MaxAttempts: intRef(0)}, {MaxAttempts: intRef(11)}, {DebounceSeconds: 1}}
	for _, schedule := range invalid {
		copy := p
		copy.Schedule = schedule
		w := request(t, s, "PATCH", "plans/"+p.ID, copy)
		if w.Code != 400 {
			t.Fatal("saved invalid policy", w.Body.String())
		}
	}
	p.Schedule = backup.Schedule{MinChangedFiles: 3, MinIntervalMinutes: intRef(0), MaxIntervalMinutes: 60, MaxAttempts: intRef(2)}
	w := request(t, s, "PATCH", "plans/"+p.ID, p)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var saved backup.Plan
	json.Unmarshal(w.Body.Bytes(), &saved)
	if saved.Schedule.MinIntervalMinutes == nil || *saved.Schedule.MinIntervalMinutes != 0 {
		t.Fatal("lost explicit zero")
	}
}

func failRsync(t *testing.T, s *Server) string {
	t.Helper()
	detected := backup.DetectRsync("")
	if !detected.Available {
		t.Skip("rsync required")
	}
	wrapper := filepath.Join(t.TempDir(), "rsync-failing")
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then exec \"" + detected.Path + "\" \"$@\"; fi\necho intentional-rsync-test-failure >&2\nexit 12\n"
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.settings.RsyncPath = wrapper
	s.mu.Unlock()
	return detected.Path
}
func ageRetry(t *testing.T, s *Server, p backup.Plan, age time.Duration) {
	t.Helper()
	cp := pendingPolicy(t, s, p)
	cp.LastAttempt = time.Now().Add(-age)
	if err := s.Store.Put("scan_checkpoints", p.ID, cp); err != nil {
		t.Fatal(err)
	}
}
func newestPolicyRun(t *testing.T, s *Server) backup.Run {
	t.Helper()
	runs, err := s.Store.Runs()
	if err != nil || len(runs) == 0 {
		t.Fatal("missing run", err)
	}
	return waitRun(t, s, runs[0].ID)
}
func TestAutomaticRetryExecutesAndStopsAtConfiguredAttemptLimit(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	p.Schedule = backup.Schedule{IntervalMinutes: 60, MaxAttempts: intRef(3)}
	s.Store.Put("backup_plans", p.ID, p)
	failRsync(t, s)
	first, err := s.startWithRequest(p, "schedule", RunRequest{AutomaticAttempt: 1})
	if err != nil {
		t.Fatal(err)
	}
	first = waitRun(t, s, first.ID)
	if first.Status != "failed" {
		t.Fatal(first)
	}
	s.tick(s.ctx)
	cp := pendingPolicy(t, s, p)
	if !cp.RetryPending || cp.Failures != 1 {
		t.Fatal("no durable retry decision", cp)
	}
	if runs, _ := s.Store.Runs(); len(runs) != 1 {
		t.Fatal("ignored failure backoff")
	}
	ageRetry(t, s, p, 2*time.Minute)
	s.tick(s.ctx)
	second := newestPolicyRun(t, s)
	if second.Status != "failed" || second.AutomaticAttempt != 2 || second.RetryOf != first.ID {
		t.Fatalf("not a real second attempt: %+v", second)
	}
	s.tick(s.ctx)
	ageRetry(t, s, p, 3*time.Minute)
	s.tick(s.ctx)
	third := newestPolicyRun(t, s)
	if third.Status != "failed" || third.AutomaticAttempt != 3 || third.RetryOf != second.ID {
		t.Fatal(third)
	}
	s.tick(s.ctx)
	s.tick(s.ctx)
	if runs, _ := s.Store.Runs(); len(runs) != 3 {
		t.Fatal("attempt limit bypassed")
	}
	if cp = pendingPolicy(t, s, p); cp.RetryPending || cp.Failures != 3 {
		t.Fatal(cp)
	}
}
func TestVolumeRetryKeepsOnlyOriginalTargetAndCanRecover(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	offline := backup.Destination{ID: backup.ID("dst-"), Name: "offline B", Enabled: true, Path: filepath.Join(t.TempDir(), "missing"), VolumeID: "missing-volume"}
	p.Destinations = append(p.Destinations, offline)
	p.Schedule = backup.Schedule{OnMount: true, MaxAttempts: intRef(2)}
	s.Store.Put("backup_plans", p.ID, p)
	s.Store.Put("scan_checkpoints", p.ID, checkpoint{TargetOnline: map[string]bool{p.Destinations[0].ID: true, offline.ID: false}, LastVolumeCheck: time.Now(), VolumeEpoch: s.mountEpoch})
	realPath := failRsync(t, s)
	r, err := s.startWithRequest(p, "volume", RunRequest{DestinationIDs: []string{p.Destinations[0].ID}, AutomaticAttempt: 1})
	if err != nil {
		t.Fatal(err)
	}
	if r = waitRun(t, s, r.ID); r.Status != "failed" {
		t.Fatal(r)
	}
	s.tick(s.ctx)
	ageRetry(t, s, p, 2*time.Minute)
	s.mu.Lock()
	s.settings.RsyncPath = realPath
	s.mu.Unlock()
	s.tick(s.ctx)
	retry := newestPolicyRun(t, s)
	if retry.Status != "success" || retry.AutomaticAttempt != 2 || len(retry.DestinationIDs) != 1 || retry.DestinationIDs[0] != p.Destinations[0].ID {
		t.Fatalf("retry lost mount scope or waited for B: %+v", retry)
	}
	s.tick(s.ctx)
	if cp := pendingPolicy(t, s, p); cp.Failures != 0 || cp.RetryPending {
		t.Fatal("success did not reset retry state", cp)
	}
}
func TestManualFailureAndConfirmationAreNotAutomaticallyRetried(t *testing.T) {
	t.Run("manual failure", func(t *testing.T) {
		s := testServer(t)
		p := saveFixturePlan(t, s, "snapshot")
		p.Schedule = backup.Schedule{OnChange: true}
		s.Store.Put("backup_plans", p.ID, p)
		failRsync(t, s)
		r := apiRun(t, s, p, RunRequest{})
		if r.Status != "failed" {
			t.Fatal(r)
		}
		s.tick(s.ctx)
		s.tick(s.ctx)
		if runs, _ := s.Store.Runs(); len(runs) != 1 {
			t.Fatal("retried manual failure")
		}
		if cp := pendingPolicy(t, s, p); cp.Failures != 0 || cp.RetryPending {
			t.Fatal(cp)
		}
	})
	t.Run("source confirmation", func(t *testing.T) {
		s := testServer(t)
		p := saveFixturePlan(t, s, "snapshot")
		p.Schedule = backup.Schedule{IntervalMinutes: 60}
		s.Store.Put("backup_plans", p.ID, p)
		root := p.Sources[0].Path
		os.Rename(root, root+"-old")
		os.Mkdir(root, 0755)
		r, err := s.startWithRequest(p, "schedule", RunRequest{AutomaticAttempt: 1})
		if err != nil {
			t.Fatal(err)
		}
		if r = waitRun(t, s, r.ID); r.Status != "needs_confirmation" {
			t.Fatal(r)
		}
		s.tick(s.ctx)
		s.tick(s.ctx)
		if runs, _ := s.Store.Runs(); len(runs) != 1 {
			t.Fatal("confirmation loop generated automatic runs")
		}
	})
}
func TestScheduleStatusReportsUnknownRatherThanInventedZeroCounts(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	w := request(t, s, "GET", "plans/"+p.ID+"/schedule", nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var body map[string]any
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["changes"] != nil || body["checkedAt"] != nil {
		t.Fatal("fabricated unchecked statistics", body)
	}
	cp := checkpoint{Decision: "below threshold", LastChangeCheck: time.Now(), Changes: &ChangeSummary{Changed: true, Files: 2, Bytes: 128}, RetryPending: true, Failures: 1, LastAttempt: time.Now()}
	s.Store.Put("scan_checkpoints", p.ID, cp)
	w = request(t, s, "GET", "plans/"+p.ID+"/schedule", nil)
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["nextRetryAt"] == nil || body["changes"].(map[string]any)["files"] != float64(2) {
		t.Fatal(body)
	}
}

func TestMaximumOnlyPolicyDoesNotScanBeforeItsDeadline(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	r := apiRun(t, s, p, RunRequest{})
	if r.Status != "success" {
		t.Fatal(r)
	}
	p.Schedule = backup.Schedule{MaxIntervalMinutes: 60}
	s.Store.Put("backup_plans", p.ID, p)
	occupyScheduler(t, s)
	s.tick(s.ctx)
	if cp := pendingPolicy(t, s, p); !cp.LastChangeCheck.IsZero() {
		t.Fatal("maximum-only policy scanned before due", cp)
	}
}
func TestAutomaticRetryPolicySurvivesServiceRestart(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	p.Schedule = backup.Schedule{IntervalMinutes: 60, MaxAttempts: intRef(2)}
	s.Store.Put("backup_plans", p.ID, p)
	failRsync(t, s)
	first, err := s.startWithRequest(p, "schedule", RunRequest{AutomaticAttempt: 1})
	if err != nil {
		t.Fatal(err)
	}
	if first = waitRun(t, s, first.ID); first.Status != "failed" {
		t.Fatal(first)
	}
	s.tick(s.ctx)
	restarted := New(s.Store, s.Token, s.Host, s.Web, false)
	restarted.tick(restarted.ctx)
	if runs, _ := s.Store.Runs(); len(runs) != 1 {
		t.Fatal("restart discarded backoff")
	}
	ageRetry(t, restarted, p, 2*time.Minute)
	restarted.tick(restarted.ctx)
	second := newestPolicyRun(t, restarted)
	if second.Status != "success" || second.AutomaticAttempt != 2 || second.RetryOf != first.ID {
		t.Fatal("lost persisted retry scope/attempt", second)
	}
}

func TestMaximumIntervalReadsUnchangedSourceContentForFullAudit(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	r := apiRun(t, s, p, RunRequest{})
	if r.Status != "success" {
		t.Fatal(r)
	}
	old := time.Now().Add(-2 * time.Hour)
	r.FinishedAt = &old
	s.Store.Put("backup_runs", r.ID, r)
	p.Schedule = backup.Schedule{MaxIntervalMinutes: 60}
	s.Store.Put("backup_plans", p.ID, p)
	occupyScheduler(t, s)
	var read int64
	ctx := backup.Observe(s.ctx, func(e backup.WorkEvent) { read += e.Bytes })
	s.tick(ctx)
	if read != 25 {
		t.Fatalf("maximum audit did not read all source content: %d", read)
	}
}

func TestNativeBelowThresholdDoesNotWalkSourceOnUnchangedRevision(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	if r := apiRun(t, s, p, RunRequest{}); r.Status != "success" {
		t.Fatal(r)
	}
	p.Schedule = backup.Schedule{OnChange: true, DebounceSeconds: 10, MinChangedFiles: 100, MinIntervalMinutes: intRef(0)}
	s.Store.Put("backup_plans", p.ID, p)
	occupyScheduler(t, s)
	os.WriteFile(filepath.Join(p.Sources[0].Path, "one.txt"), []byte("changed"), 0644)
	s.Store.DB.Exec("INSERT INTO change_events(plan_id,source_id,relative_path,event_id,flags,requires_scan,changed_at_ns) VALUES(?,?,?,?,?,?,?)", p.ID, p.Sources[0].ID, "one.txt", "9", 0, 0, time.Now().Add(-time.Minute).UnixNano())
	s.watchMu.Lock()
	s.watcher = WatchStatus{Backend: "fsevents", Healthy: true}
	s.watchMu.Unlock()
	s.tick(s.ctx)
	cp := pendingPolicy(t, s, p)
	source := p.Sources[0].Path
	hidden := source + "-probe-no-enumeration"
	if err := os.Rename(source, hidden); err != nil {
		t.Fatal(err)
	}
	defer os.Rename(hidden, source)
	// No native watcher is actually started in this test; therefore no new dirty
	// revision is delivered. An idle below-threshold tick must not touch roots.
	s.tick(s.ctx)
	after := pendingPolicy(t, s, p)
	if !after.LastChangeCheck.Equal(cp.LastChangeCheck) || after.Decision != cp.Decision {
		t.Fatalf("idle native tick accessed the source: %q -> %q", cp.Decision, after.Decision)
	}
}
