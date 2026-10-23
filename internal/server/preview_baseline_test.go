package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"localbackup/internal/backup"
)

func TestPreviewAfterBackupDoesNotReadHistoricalContents(t *testing.T) {
	for _, mode := range []string{"snapshot", "incremental", "mirror"} {
		t.Run(mode, func(t *testing.T) {
			s := testServer(t)
			p := saveFixturePlan(t, s, mode)
			if r := apiRun(t, s, p, RunRequest{}); r.Status != "success" {
				t.Fatal(r.Error)
			}
			sn := s.latest(p.ID, p.Destinations[0].ID)
			if sn == nil {
				t.Fatal("missing first snapshot")
			}
			// Source hashing may legitimately occur on uncacheable filesystems;
			// historical hashing must never occur during preview in any mode.
			ctx := backup.Observe(context.Background(), func(e backup.WorkEvent) {
				if e.Path != "" && backup.Contains(sn.Path, e.Path) {
					t.Errorf("preview opened historical content: %s", e.Path)
				}
			})
			for i := 0; i < 2; i++ {
				v, err := s.createPreview(ctx, p, nil)
				if err != nil {
					t.Fatal(err)
				}
				if v.PreviewID == "" || v.Added != 0 || v.Modified != 0 || v.Deleted != 0 || v.Reused != 2 {
					t.Fatalf("incorrect unchanged preview: %+v", v)
				}
			}
			if err := backup.AuditSnapshot(context.Background(), *sn); err != nil {
				t.Fatal("preview changed history", err)
			}
		})
	}
}

func TestExecutionAuditsHistoricalContentsAfterLightweightPreview(t *testing.T) {
	for _, mode := range []string{"incremental", "mirror"} {
		t.Run(mode, func(t *testing.T) {
			s := testServer(t)
			p := saveFixturePlan(t, s, mode)
			if r := apiRun(t, s, p, RunRequest{}); r.Status != "success" {
				t.Fatal(r.Error)
			}
			sn := s.latest(p.ID, p.Destinations[0].ID)
			if sn == nil {
				t.Fatal("missing first snapshot")
			}
			v := getPreview(t, s, p)
			historical := filepath.Join(sn.Path, p.Sources[0].ID, "one.txt")
			info, err := os.Stat(historical)
			if err != nil {
				t.Fatal(err)
			}
			// Simulate external corruption after approval, preserving size/mtime.
			if err := os.WriteFile(historical, []byte(strings.Repeat("x", int(info.Size()))), info.Mode().Perm()); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(historical, info.ModTime(), info.ModTime()); err != nil {
				t.Fatal(err)
			}
			// Preview remains a manifest comparison, not a content integrity audit.
			getPreview(t, s, p)
			r := apiRun(t, s, p, RunRequest{PreviewID: v.PreviewID})
			if r.Status != "failed" || !strings.Contains(r.Error, "目标基线完整性校验失败") || !strings.Contains(r.Error, "内容校验失败") {
				t.Fatalf("corrupted baseline escaped execution audit: %+v", r)
			}
			snapshots, err := s.Store.Snapshots()
			if err != nil || len(snapshots) != 1 {
				t.Fatalf("failed audit published a snapshot: %d, %v", len(snapshots), err)
			}
			current, err := backup.ReadCurrent(p.Destinations[0], p.ID)
			if err != nil || current == nil || current.ID != sn.ID {
				t.Fatalf("failed audit changed current: %+v, %v", current, err)
			}
			data, err := os.ReadFile(historical)
			if err != nil || string(data) != strings.Repeat("x", int(info.Size())) {
				t.Fatal("failed execution modified historical content", err)
			}
		})
	}
}
