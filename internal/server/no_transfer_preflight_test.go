package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnchangedRunNeverSubmitsSourceManifestButRealTransferStillDoes(t *testing.T) {
	s := testServer(t)
	p := savedPreviewPlan(t, s)
	_, calls, fail := countedRsync(t, s)
	if r := apiRun(t, s, p, RunRequest{}); r.Status != "success" {
		t.Fatal(r.Error)
	}
	before := preflightCalls(t, calls)
	// The synthetic capability check is already warm, but an actual full-manifest
	// preflight would fail now. A read-only no-change check needs no transfer.
	if err := os.WriteFile(fail, nil, 0600); err != nil {
		t.Fatal(err)
	}
	r := apiRun(t, s, p, RunRequest{})
	if r.Status != "success" || !r.NoChanges || preflightCalls(t, calls) != before {
		t.Fatal("unchanged execution redundantly invoked transfer tool", r.Error)
	}
	log := strings.Join(r.Logs, "\n")
	if !strings.Contains(log, "已有恢复点内容校验完成") || !strings.Contains(log, "源文件内容校验完成") || !strings.Contains(log, "额外内容读取 0 B") {
		t.Fatal("missing complete verification evidence", log)
	}
	if err := os.WriteFile(filepath.Join(p.Sources[0].Path, "changed.txt"), []byte("new contents"), 0644); err != nil {
		t.Fatal(err)
	}
	r = apiRun(t, s, p, RunRequest{})
	if r.Status != "failed" || r.NoChanges || preflightCalls(t, calls) <= before {
		t.Fatal("changed run skipped fresh full preflight", r.Error)
	}
	snapshots, err := s.Store.Snapshots()
	if err != nil || len(snapshots) != 1 {
		t.Fatal("failed preflight published generation", err)
	}
}
