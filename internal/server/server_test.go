package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"localbackup/internal/backup"
	"localbackup/internal/store"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	st, e := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { st.DB.Close() })
	return New(st, "test-token", "127.0.0.1:8787", "../../apps/web/dist", false)
}
func request(t *testing.T, s *Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var b bytes.Buffer
	if body != nil {
		json.NewEncoder(&b).Encode(body)
	}
	r := httptest.NewRequest(method, "http://127.0.0.1:8787/api/v1/"+path, &b)
	r.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}
func planFixture(t *testing.T) backup.Plan {
	base := t.TempDir()
	dirs := []string{filepath.Join(base, "a"), filepath.Join(base, "b"), filepath.Join(base, "target")}
	for _, d := range dirs {
		os.Mkdir(d, 0755)
	}
	os.WriteFile(filepath.Join(dirs[0], "one.txt"), []byte("first source"), 0644)
	os.WriteFile(filepath.Join(dirs[1], "two.txt"), []byte("second source"), 0644)
	return backup.Plan{Name: "Integration", Mode: "snapshot", Enabled: true, Sources: []backup.Source{{Name: "A", Path: dirs[0], TargetPath: "A", Enabled: true}, {Name: "B", Path: dirs[1], TargetPath: "B", Enabled: true}}, Destinations: []backup.Destination{{Name: "Target", Path: dirs[2], Enabled: true}}, Retention: backup.Retention{KeepLast: 1}}
}
func TestAuthenticationOriginAndHost(t *testing.T) {
	s := testServer(t)
	for _, tc := range []struct {
		token, origin, host string
		want                int
	}{{"", "", "127.0.0.1:8787", 401}, {"test-token", "https://evil.example", "127.0.0.1:8787", 403}, {"test-token", "", "evil.example", 403}, {"test-token", "http://127.0.0.1:8787", "127.0.0.1:8787", 200}} {
		r := httptest.NewRequest("GET", "http://"+tc.host+"/api/v1/plans", nil)
		r.Header.Set("Authorization", "Bearer "+tc.token)
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("got %d want %d: %s", w.Code, tc.want, w.Body)
		}
	}
}
func TestAPIEndToEnd(t *testing.T) {
	s := testServer(t)
	p := planFixture(t)
	w := request(t, s, "POST", "plans", p)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &p)
	w = request(t, s, "POST", "plans/"+p.ID+"/preview", map[string]any{})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var preview backup.Preview
	json.Unmarshal(w.Body.Bytes(), &preview)
	if preview.Added != 2 {
		t.Fatalf("bad preview %+v", preview)
	}
	for i := 0; i < 2; i++ {
		if i == 1 {
			if err := os.WriteFile(filepath.Join(p.Sources[0].Path, "one.txt"), []byte("changed source version"), 0644); err != nil {
				t.Fatal(err)
			}
		}
		w = request(t, s, "POST", "plans/"+p.ID+"/runs", map[string]any{})
		if w.Code != 202 {
			t.Fatal(w.Body.String())
		}
		var run backup.Run
		json.Unmarshal(w.Body.Bytes(), &run)
		deadline := time.Now().Add(15 * time.Second)
		for {
			if time.Now().After(deadline) {
				t.Fatal("backup timed out")
			}
			time.Sleep(20 * time.Millisecond)
			if e := s.Store.Get("backup_runs", run.ID, &run); e != nil {
				t.Fatal(e)
			}
			if run.FinishedAt != nil {
				break
			}
		}
		if run.Status != "success" {
			t.Fatalf("run failed %+v", run)
		}
		for s.busy() {
			time.Sleep(time.Millisecond)
		}
	}
	snaps, _ := s.Store.Snapshots()
	if len(snaps) != 2 {
		t.Fatal("snapshots not persisted")
	}
	w = request(t, s, "GET", "snapshots/"+snaps[0].ID+"/files", nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = request(t, s, "POST", "restores", backup.RestoreRequest{SnapshotID: snaps[0].ID, SourceID: p.Sources[0].ID, Path: "one.txt", Destination: t.TempDir()})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = request(t, s, "POST", "retention/preview", map[string]any{"planId": p.ID})
	var candidates []backup.Snapshot
	json.Unmarshal(w.Body.Bytes(), &candidates)
	if len(candidates) != 1 {
		t.Fatalf("bad retention %s", w.Body)
	}
	w = request(t, s, "POST", "retention/apply", map[string]any{"planId": p.ID, "ids": []string{snaps[0].ID}, "confirm": true})
	if w.Code != 409 {
		t.Fatal("allowed latest snapshot deletion")
	}
	w = request(t, s, "POST", "retention/apply", map[string]any{"planId": p.ID, "ids": []string{candidates[0].ID}, "confirm": true})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	snaps, _ = s.Store.Snapshots()
	if len(snaps) != 1 {
		t.Fatal("retention did not remove old snapshot")
	}
	if e := backup.Verify(context.Background(), snaps[0].Path, snaps[0].Entries); e != nil {
		t.Fatalf("retention damaged latest: %v", e)
	}
	w = request(t, s, "DELETE", "plans/"+p.ID, nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	snaps, _ = s.Store.Snapshots()
	if len(snaps) != 1 {
		t.Fatal("deleting plan removed history")
	}
}
func TestRestartRecovery(t *testing.T) {
	s := testServer(t)
	r := backup.Run{ID: "run-interrupted", PlanID: "test", Status: "running"}
	s.Store.Put("backup_runs", r.ID, r)
	New(s.Store, "token", "127.0.0.1:8787", "", false)
	s.Store.Get("backup_runs", r.ID, &r)
	if r.Status != "recoverable" || r.FinishedAt == nil {
		t.Fatal("interrupted run not recovered")
	}
}
func TestCrossPlanRecursionRejected(t *testing.T) {
	s := testServer(t)
	p := planFixture(t)
	w := request(t, s, "POST", "plans", p)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	q := planFixture(t)
	q.Destinations[0].Path = filepath.Join(p.Sources[0].Path, "nested")
	os.Mkdir(q.Destinations[0].Path, 0755)
	w = request(t, s, "POST", "plans", q)
	if w.Code != 400 {
		t.Fatal("cross-plan recursion accepted")
	}
}

func TestRepositoryManifestRecovery(t *testing.T) {
	s := testServer(t)
	p := planFixture(t)
	p.ID = "plan-recovery"
	if e := backup.Validate(&p); e != nil {
		t.Fatal(e)
	}
	s.Store.Put("backup_plans", p.ID, p)
	entries, _, e := backup.Scan(context.Background(), p)
	if e != nil {
		t.Fatal(e)
	}
	sn, e := backup.Execute(context.Background(), p, p.Destinations[0], entries, nil, backup.DetectRsync(""), func(string) {})
	if e != nil {
		t.Fatal(e)
	}
	all, _ := s.Store.Snapshots()
	if len(all) != 0 {
		t.Fatal("fixture unexpectedly indexed")
	}
	restarted := New(s.Store, "test-token", "127.0.0.1:8787", "", false)
	restarted.reconcile(context.Background())
	all, _ = restarted.Store.Snapshots()
	if len(all) != 1 || all[0].ID != sn.ID {
		t.Fatal("committed snapshot not recovered from manifest")
	}
	restarted.Store.Delete("snapshots", sn.ID)
	w := request(t, s, "POST", "repositories/import", map[string]string{"path": p.Destinations[0].Path})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var result recoveryResult
	json.Unmarshal(w.Body.Bytes(), &result)
	if result.Imported != 1 {
		t.Fatalf("import failed: %s", w.Body.String())
	}
}

func TestSchedulerDebounceAndExcludedChanges(t *testing.T) {
	s := testServer(t)
	p := planFixture(t)
	p.ID = "plan-scheduler"
	p.Schedule = backup.Schedule{OnChange: true, DebounceSeconds: 10}
	p.Rules = []backup.Rule{{Action: "exclude", Pattern: "*.tmp"}}
	if e := backup.Validate(&p); e != nil {
		t.Fatal(e)
	}
	s.Store.Put("backup_plans", p.ID, p)
	ctx := context.Background()
	s.tick(ctx)
	var cp checkpoint
	s.Store.Get("scan_checkpoints", p.ID, &cp)
	first := cp.Signature
	if first == "" {
		t.Fatal("missing baseline")
	}
	os.WriteFile(filepath.Join(p.Sources[0].Path, "excluded.tmp"), []byte("ignored"), 0644)
	sig, e := signature(ctx, p)
	if e != nil || sig != first {
		t.Fatal("excluded file caused change trigger")
	}
	os.WriteFile(filepath.Join(p.Sources[0].Path, "one.txt"), []byte("updated content"), 0644)
	s.tick(ctx)
	runs, _ := s.Store.Runs()
	if len(runs) != 0 {
		t.Fatal("debounce did not wait")
	}
	s.Store.Get("scan_checkpoints", p.ID, &cp)
	if cp.ChangedAt.IsZero() {
		t.Fatal("change not recorded")
	}
	cp.ChangedAt = time.Now().Add(-time.Minute)
	s.Store.Put("scan_checkpoints", p.ID, cp)
	s.tick(ctx)
	deadline := time.Now().Add(10 * time.Second)
	for s.busy() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	runs, _ = s.Store.Runs()
	if len(runs) != 1 || runs[0].Trigger != "change" || runs[0].Status != "success" {
		t.Fatalf("automatic backup failed %+v", runs)
	}
}
func TestOfflinePlanCanBePaused(t *testing.T) {
	s := testServer(t)
	p := planFixture(t)
	w := request(t, s, "POST", "plans", p)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &p)
	os.RemoveAll(p.Destinations[0].Path)
	p.Enabled = false
	w = request(t, s, "PATCH", "plans/"+p.ID, p)
	if w.Code != 200 {
		t.Fatalf("cannot pause offline plan: %s", w.Body.String())
	}
}
