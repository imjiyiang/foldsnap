package store

import (
	"context"
	"fmt"
	"localbackup/internal/backup"
	"path/filepath"
	"testing"
	"time"
)

func TestSummaryQueriesPreserveFullRecords(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	older := backup.Snapshot{ID: "old", PlanID: "p1", CreatedAt: time.Now().Add(-time.Hour), Files: 1, Entries: []backup.Entry{{Path: "secret"}}}
	newer := older
	newer.ID = "new"
	newer.PlanID = "p2"
	newer.CreatedAt = time.Now()
	for _, v := range []backup.Snapshot{newer, older} {
		if err = s.Put("snapshots", v.ID, v); err != nil {
			t.Fatal(err)
		}
	}
	out, err := s.SnapshotSummaries(context.Background(), "")
	if err != nil || len(out) != 2 || out[0].ID != "new" || len(out[0].Entries) != 0 || out[0].Files != 1 {
		t.Fatal(out, err)
	}
	filtered, err := s.SnapshotSummaries(context.Background(), "p1")
	if err != nil || len(filtered) != 1 || filtered[0].ID != "old" {
		t.Fatal(filtered, err)
	}
	run := backup.Run{ID: "r1", Status: "success", Logs: []string{"complete"}, Preview: &backup.Preview{Added: 1, Operations: []backup.Operation{{Path: "secret"}}, Removals: []backup.Operation{{Path: "removed"}}}}
	if err = s.Put("backup_runs", run.ID, run); err != nil {
		t.Fatal(err)
	}
	runs, err := s.RunSummaries(context.Background())
	if err != nil || len(runs) != 1 || runs[0].Preview.Added != 1 || len(runs[0].Logs) != 1 || len(runs[0].Preview.Operations) != 0 || len(runs[0].Preview.Removals) != 0 {
		t.Fatal(runs, err)
	}
	var full backup.Run
	if err = s.Get("backup_runs", run.ID, &full); err != nil || len(full.Preview.Operations) != 1 {
		t.Fatal(full, err)
	}
	latest, err := s.LatestSnapshot(context.Background(), "p1", "")
	if err != nil || latest == nil || latest.ID != "old" || len(latest.Entries) != 1 {
		t.Fatal(latest, err)
	}
	missing, err := s.LatestSnapshot(context.Background(), "missing", "")
	if err != nil || missing != nil {
		t.Fatal(missing, err)
	}
	fullSnapshots, err := s.Snapshots()
	if err != nil || len(fullSnapshots[0].Entries) != 1 {
		t.Fatal(fullSnapshots, err)
	}
}

func BenchmarkSnapshotList(b *testing.B) {
	s, err := Open(filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer s.DB.Close()
	entries := make([]backup.Entry, 10000)
	for i := range entries {
		entries[i] = backup.Entry{Path: fmt.Sprintf("folder/file-%06d.txt", i), Kind: "file", Size: 4096, Hash: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
	}
	for i := 0; i < 5; i++ {
		v := backup.Snapshot{ID: fmt.Sprint(i), PlanID: "p1", CreatedAt: time.Now(), Entries: entries}
		if err = s.Put("snapshots", v.ID, v); err != nil {
			b.Fatal(err)
		}
	}
	b.Run("full_decode", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := s.Snapshots(); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("summary", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := s.SnapshotSummaries(context.Background(), ""); err != nil {
				b.Fatal(err)
			}
		}
	})
}
