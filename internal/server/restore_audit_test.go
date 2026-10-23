package server

import (
	"encoding/json"
	"localbackup/internal/backup"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func restoreAuditFixture(t *testing.T) (*Server, backup.Plan, backup.RestoreRequest) {
	t.Helper()
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	if r := apiRun(t, s, p, RunRequest{}); r.Status != "success" {
		t.Fatal(r.Error)
	}
	sn := s.latest(p.ID, p.Destinations[0].ID)
	return s, p, backup.RestoreRequest{SnapshotID: sn.ID, SourceID: p.Sources[0].ID, Path: "one.txt", Destination: t.TempDir()}
}

func TestRestoreFailsClosedWhenPlanReadFails(t *testing.T) {
	s, _, req := restoreAuditFixture(t)
	// Valid JSON with an invalid Plan field simulates a damaged configuration.
	if _, err := s.Store.DB.Exec(`UPDATE backup_plans SET data='{"sources":false}'`); err != nil {
		t.Fatal(err)
	}
	w := request(t, s, "POST", "restores", req)
	if w.Code != 500 || !strings.Contains(w.Body.String(), "无法读取计划") {
		t.Fatal(w.Code, w.Body.String())
	}
	children, err := os.ReadDir(req.Destination)
	if err != nil || len(children) != 0 {
		t.Fatal("restore wrote before validating all plans", children, err)
	}
	if s.busy() {
		t.Fatal("failed validation left service busy")
	}
}

func TestRestoreAuditFailureCannotReportSuccess(t *testing.T) {
	s, _, req := restoreAuditFixture(t)
	if _, err := s.Store.DB.Exec(`CREATE TRIGGER fail_restore_completion BEFORE UPDATE ON restore_jobs WHEN json_extract(NEW.data,'$.status')='success' BEGIN SELECT RAISE(ABORT,'injected audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	w := request(t, s, "POST", "restores", req)
	if w.Code != 500 || !strings.Contains(w.Body.String(), "无法保存结果") {
		t.Fatal(w.Code, w.Body.String())
	}
	children, err := os.ReadDir(req.Destination)
	if err != nil || len(children) != 1 {
		t.Fatal("completed output must remain", children, err)
	}
	content, err := os.ReadFile(filepath.Join(req.Destination, children[0].Name(), "one.txt"))
	if err != nil || string(content) != "first source" {
		t.Fatal("completed output damaged", string(content), err)
	}
	if !strings.Contains(w.Body.String(), children[0].Name()) {
		t.Fatal("response did not identify completed output")
	}
	if s.busy() {
		t.Fatal("failed audit left service busy")
	}
}

func TestRestoreCannotCreateOutputBeforeAudit(t *testing.T) {
	s, _, req := restoreAuditFixture(t)
	if _, err := s.Store.DB.Exec(`CREATE TRIGGER fail_restore_path BEFORE UPDATE ON restore_jobs WHEN json_extract(NEW.data,'$.status')='running' AND json_extract(NEW.data,'$.path') IS NOT NULL BEGIN SELECT RAISE(ABORT,'injected path audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	w := request(t, s, "POST", "restores", req)
	if w.Code == 200 || !strings.Contains(w.Body.String(), "无法记录恢复输出位置") {
		t.Fatal(w.Code, w.Body.String())
	}
	children, err := os.ReadDir(req.Destination)
	if err != nil || len(children) != 0 {
		t.Fatal("output created before durable audit", children, err)
	}
}

func TestRestartClassifiesInterruptedRestoreAndKeepsOutput(t *testing.T) {
	s := testServer(t)
	output := filepath.Join(t.TempDir(), "partial")
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(output, "kept.txt")
	if err := os.WriteFile(file, []byte("keep partial output"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ id, status, kind string }{
		{"restore-old", "running", ""}, {"restore-new", "running", "restore"},
		{"restore-complete", "success", "restore"}, {"mkdir-other", "running", "create-directory"},
	} {
		job := map[string]any{"id": row.id, "type": row.kind, "status": row.status, "path": output}
		if row.kind == "" {
			job["request"] = map[string]string{"snapshotId": "snapshot-old"}
		}
		if err := s.Store.Put("restore_jobs", row.id, job); err != nil {
			t.Fatal(err)
		}
	}
	New(s.Store, "test-token", "127.0.0.1:8787", "", false)
	for _, id := range []string{"restore-old", "restore-new"} {
		var got map[string]any
		if err := s.Store.Get("restore_jobs", id, &got); err != nil {
			t.Fatal(err)
		}
		if got["status"] != "interrupted" || got["path"] != output || got["finishedAt"] == nil {
			t.Fatal(got)
		}
	}
	for id, want := range map[string]string{"restore-complete": "success", "mkdir-other": "running"} {
		var got map[string]any
		if err := s.Store.Get("restore_jobs", id, &got); err != nil || got["status"] != want {
			t.Fatal(got, err)
		}
	}
	content, err := os.ReadFile(file)
	if err != nil || string(content) != "keep partial output" {
		t.Fatal("restart changed output", err)
	}
}

func TestRestoreHistoryExcludesOtherAudits(t *testing.T) {
	s := testServer(t)
	for i := 0; i < 55; i++ {
		id := backup.ID("restore-")
		if err := s.Store.Put("restore_jobs", id, map[string]any{"id": id, "type": "restore", "status": "success"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Store.Put("restore_jobs", "mkdir-test", map[string]string{"type": "create-directory", "status": "success"}); err != nil {
		t.Fatal(err)
	}
	w := request(t, s, "GET", "restores", nil)
	var jobs []map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &jobs) != nil || len(jobs) != 50 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, j := range jobs {
		if j["type"] != "restore" {
			t.Fatal("unrelated audit exposed", j)
		}
	}
}
