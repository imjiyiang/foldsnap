package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"localbackup/internal/backup"
)

func TestCachedScanMutationBeforeFinalVersionCheckDoesNotCommit(t *testing.T) {
	for _, change := range []string{"same-size restored-mtime content", "symlink replacement", "permissions", "attributes", "deleted"} {
		t.Run(change, func(t *testing.T) {
			st, p := indexedFixture(t)
			// A previous sibling reuses the same parent rule decision before
			// the target is checked; this must never reuse file metadata.
			parent := filepath.Join(p.Sources[0].Path, "nested", "deeper")
			if err := os.MkdirAll(parent, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(parent, "a-first.txt"), []byte("sibling"), 0644); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(parent, "file.txt")
			if err := os.WriteFile(path, []byte("AAA"), 0644); err != nil {
				t.Fatal(err)
			}
			indexedScan(t, st, p, false)
			before, err := st.IndexStatus(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !before.Sources[0].Cacheable {
				t.Skip("requires cacheable filesystem")
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(t.TempDir(), "outside.txt")
			if err := os.WriteFile(outside, []byte("outside unchanged"), 0644); err != nil {
				t.Fatal(err)
			}
			changed := false
			ctx := backup.Observe(context.Background(), func(e backup.WorkEvent) {
				if changed || !e.CacheHit || e.Path != path {
					return
				}
				changed = true
				switch change {
				case "same-size restored-mtime content":
					err = os.WriteFile(path, []byte("BBB"), 0644)
					if err == nil {
						err = os.Chtimes(path, info.ModTime(), info.ModTime())
					}
				case "symlink replacement":
					err = os.Remove(path)
					if err == nil {
						err = os.Symlink(outside, path)
					}
				case "permissions":
					err = os.Chmod(path, 0600)
				case "attributes":
					err = unix.Lsetxattr(path, "user.localbackup-test", []byte("changed"), 0)
				case "deleted":
					err = os.Remove(path)
				}
			})
			_, scanErr := backup.ScanIndexed(ctx, p, st, false)
			if err != nil {
				t.Fatal("mutation fixture failed", err)
			}
			if !changed || scanErr == nil {
				t.Fatalf("changed cached file was accepted: changed=%v error=%v", changed, scanErr)
			}
			after, err := st.IndexStatus(context.Background())
			if err != nil || after.Paths != before.Paths || after.Sources[0].Generation != before.Sources[0].Generation {
				t.Fatal("failed version check committed index", err)
			}
			data, err := os.ReadFile(outside)
			if err != nil || string(data) != "outside unchanged" {
				t.Fatal("outside file changed", err)
			}
		})
	}
}

func TestBatchedIndexFailureRollsBackEarlierBatches(t *testing.T) {
	st, p := indexedFixture(t)
	for i := 0; i < 600; i++ {
		if err := os.WriteFile(filepath.Join(p.Sources[0].Path, fmt.Sprintf("file-%04d", i)), []byte("data"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	indexedScan(t, st, p, false)
	base, err := st.LoadSourceProjection(context.Background(), p.Sources[0].ID)
	if err != nil || base == nil {
		t.Fatal("missing trusted baseline", err)
	}
	var objectsBefore int
	if err := st.DB.QueryRow("SELECT count(*) FROM filesystem_objects").Scan(&objectsBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`CREATE TRIGGER fail_later_batch BEFORE UPDATE ON source_paths WHEN NEW.relative_path='file-0300' BEGIN SELECT RAISE(ABORT,'injected later batch failure'); END`); err != nil {
		t.Fatal(err)
	}
	paths := append([]backup.IndexedPath(nil), base.Paths...)
	for i := range paths {
		paths[i].Entry.Mode = 0600
	}
	scope := base.Scope
	scope.Generation, err = st.BeginSource(context.Background(), scope.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	scope.ScannedAt = time.Now().UTC()
	err = st.CommitSource(context.Background(), scope, paths)
	if err == nil || !strings.Contains(err.Error(), "injected later batch failure") {
		t.Fatal("failure fixture did not fire", err)
	}
	after, err := st.LoadSourceProjection(context.Background(), scope.SourceID)
	if err != nil || after == nil || after.Scope.Generation != base.Scope.Generation || len(after.Paths) != 600 {
		t.Fatal("partial index published", err)
	}
	for _, record := range after.Paths {
		if record.Entry.Mode != 0644 {
			t.Fatal("earlier batch survived rollback")
		}
	}
	var objectsAfter int
	if err := st.DB.QueryRow("SELECT count(*) FROM filesystem_objects").Scan(&objectsAfter); err != nil || objectsAfter != objectsBefore {
		t.Fatal("partial object cache survived rollback", err)
	}
}
