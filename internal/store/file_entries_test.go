package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"localbackup/internal/backup"
)

func BenchmarkUnchangedFileCatalog(b *testing.B) {
	for _, rewrite := range []bool{true, false} {
		name := "diff"
		if rewrite {
			name = "rewrite"
		}
		b.Run(name, func(b *testing.B) {
			s, err := Open(filepath.Join(b.TempDir(), "catalog.sqlite"))
			if err != nil {
				b.Fatal(err)
			}
			defer s.DB.Close()
			sources := []backup.Source{{ID: "source", Enabled: true}}
			entries := make([]backup.Entry, 38356)
			for i := range entries {
				entries[i] = backup.Entry{SourceID: "source", Path: fmt.Sprintf("models/nested-%d/file-%d.bin", i/100, i), Kind: "file", Size: 32768, Hash: "f149b34c0d3f468369a49b06f13fbb6a13cba998aaee73f6a46bd8e149b044e1", ModTime: 1772803482289194654, Mode: 0644}
			}
			if _, err = s.SyncFileEntries(context.Background(), sources, entries); err != nil {
				b.Fatal(err)
			}
			if _, err = s.DB.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if !rewrite {
					if _, err = s.SyncFileEntries(context.Background(), sources, entries); err != nil {
						b.Fatal(err)
					}
					continue
				}
				tx, err := s.DB.Begin()
				if err != nil {
					b.Fatal(err)
				}
				if _, err = tx.Exec("DELETE FROM file_entries WHERE source_id='source'"); err != nil {
					b.Fatal(err)
				}
				for _, en := range entries {
					data, _ := json.Marshal(en)
					if _, err = tx.Exec("INSERT OR REPLACE INTO file_entries VALUES(?,?,?)", en.SourceID, en.Path, string(data)); err != nil {
						b.Fatal(err)
					}
				}
				if err = tx.Commit(); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			st, err := os.Stat(s.Path + "-wal")
			if err != nil {
				b.Fatal(err)
			}
			b.ReportMetric(float64(st.Size()), "WAL-B")
		})
	}
}

func TestFileEntrySyncNoopAndScopedDeltas(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "catalog.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	s.DB.SetMaxOpenConns(1)
	sources := []backup.Source{{ID: "a", Enabled: true}, {ID: "b", Enabled: true}}
	entries := []backup.Entry{{SourceID: "a", Path: "same", Hash: "one"}, {SourceID: "a", Path: "removed"}, {SourceID: "b", Path: "keep"}}
	got, err := s.SyncFileEntries(context.Background(), sources, entries)
	if err != nil || got.Added != 3 {
		t.Fatal(got, err)
	}
	if _, err = s.DB.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	var before, after int
	if err = s.DB.QueryRow("SELECT total_changes()").Scan(&before); err != nil {
		t.Fatal(err)
	}
	got, err = s.SyncFileEntries(context.Background(), sources, entries)
	if err != nil || got != (FileEntryChanges{Unchanged: 3}) {
		t.Fatal(got, err)
	}
	if err = s.DB.QueryRow("SELECT total_changes()").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("unchanged catalog mutated rows", before, after)
	}
	if st, err := os.Stat(s.Path + "-wal"); err != nil || st.Size() != 0 {
		t.Fatal("unchanged catalog wrote WAL", st, err)
	}
	sources[1].Enabled = false
	entries = []backup.Entry{{SourceID: "a", Path: "same", Hash: "two"}, {SourceID: "a", Path: "new"}}
	got, err = s.SyncFileEntries(context.Background(), sources, entries)
	if err != nil || got != (FileEntryChanges{Added: 1, Updated: 1, Deleted: 1}) {
		t.Fatal(got, err)
	}
	var count int
	s.DB.QueryRow("SELECT count(*) FROM file_entries WHERE source_id='b'").Scan(&count)
	if count != 1 {
		t.Fatal("disabled scope changed")
	}
	got, err = s.SyncFileEntries(context.Background(), sources, nil)
	if err != nil || got.Deleted != 2 {
		t.Fatal(got, err)
	}
}

func TestFileEntrySyncRollbackAndInvalidScopes(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "catalog.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	sources := []backup.Source{{ID: "a", Enabled: true}}
	first := []backup.Entry{{SourceID: "a", Path: "original", Hash: "old"}}
	if _, err = s.SyncFileEntries(context.Background(), sources, first); err != nil {
		t.Fatal(err)
	}
	// Force a failure after the write starts; the complete original catalog must survive.
	_, err = s.DB.Exec("CREATE TRIGGER fail_catalog BEFORE INSERT ON file_entries BEGIN SELECT RAISE(ABORT,'injected failure'); END")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SyncFileEntries(context.Background(), sources, []backup.Entry{{SourceID: "a", Path: "original", Hash: "changed"}, {SourceID: "a", Path: "new"}}); err == nil {
		t.Fatal("write failure ignored")
	}
	s.DB.Exec("DROP TRIGGER fail_catalog")
	for _, bad := range [][]backup.Entry{
		{{SourceID: "outside", Path: "file"}},
		{{SourceID: "a", Path: "../file"}},
		{{SourceID: "a", Path: "file"}, {SourceID: "a", Path: "file"}},
	} {
		if _, err = s.SyncFileEntries(context.Background(), sources, bad); err == nil {
			t.Fatal("invalid scope accepted", bad)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = s.SyncFileEntries(ctx, sources, nil); err == nil {
		t.Fatal("cancelled sync succeeded")
	}
	var raw string
	if err = s.DB.QueryRow("SELECT data FROM file_entries").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(first[0])
	if raw != string(want) {
		t.Fatal("failed sync changed committed catalog")
	}
	var n int
	s.DB.QueryRow("SELECT count(*) FROM file_entries").Scan(&n)
	if n != 1 {
		t.Fatal(n)
	}
}
