package store

import (
	"context"
	"errors"
	"fmt"
	"localbackup/internal/backup"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func subtreeFixture(t *testing.T) (*Store, backup.Plan) {
	t.Helper()
	st, p := indexedFixture(t)
	root := p.Sources[0].Path
	for _, dir := range []string{"hot/nested", "cold/nested"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0755); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 8; i++ {
			if err := os.WriteFile(filepath.Join(root, dir, fmt.Sprintf("%d.txt", i)), []byte(fmt.Sprint(i)), 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
	first := indexedScan(t, st, p, false)
	if first.Stats.PathsVisited != 20 {
		t.Fatalf("fixture paths %+v", first.Stats)
	}
	base, err := st.LoadSourceProjection(context.Background(), p.Sources[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if base == nil || !base.Scope.Cacheable {
		t.Skip("requires APFS signed projection")
	}
	p.RuntimeDirtyRoots = map[string][]string{p.Sources[0].ID: {"hot/nested/0.txt"}}
	return st, p
}
func assertFullEquivalent(t *testing.T, st *Store, p backup.Plan, partial backup.ScanResult) {
	t.Helper()
	p.RuntimeDirtyRoots = nil
	full := indexedScan(t, st, p, true)
	if !backup.EquivalentTrees(partial.Entries, full.Entries) {
		t.Fatalf("subtree differs from full scan: partial=%+v full=%+v", partial.Entries, full.Entries)
	}
}
func TestSubtreeScanVisitsOnlyDirtyBranchAndRefreshesAncestors(t *testing.T) {
	st, p := subtreeFixture(t)
	os.WriteFile(filepath.Join(p.Sources[0].Path, "hot/nested/0.txt"), []byte("changed"), 0644)
	partial := indexedScan(t, st, p, false)
	if partial.Stats.SubtreeSources != 1 || partial.Stats.FullSources != 0 || partial.Stats.PathsVisited != 10 || partial.Stats.ProjectedPaths != 10 || partial.Stats.FilesHashed != 1 {
		t.Fatalf("not a bounded filesystem walk: %+v", partial.Stats)
	}
	assertFullEquivalent(t, st, p, partial)
	// An authenticated empty dirty set enumerates no descendants.
	p.RuntimeDirtyRoots[p.Sources[0].ID] = []string{}
	idle := indexedScan(t, st, p, false)
	if idle.Stats.PathsVisited != 0 || idle.Stats.ProjectedPaths != 20 || idle.Stats.SubtreeSources != 1 {
		t.Fatal(idle.Stats)
	}
}
func TestSubtreeDeletionRenameAndSymlinkReplacement(t *testing.T) {
	for _, kind := range []string{"delete", "rename", "link"} {
		t.Run(kind, func(t *testing.T) {
			st, p := subtreeFixture(t)
			root := p.Sources[0].Path
			var err error
			switch kind {
			case "delete":
				err = os.Remove(filepath.Join(root, "hot/nested/0.txt"))
			case "rename":
				err = os.Rename(filepath.Join(root, "hot/nested/0.txt"), filepath.Join(root, "cold/nested/moved.txt"))
				p.RuntimeDirtyRoots[p.Sources[0].ID] = append(p.RuntimeDirtyRoots[p.Sources[0].ID], "cold/nested/moved.txt")
			case "link":
				outside := t.TempDir()
				os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("never follow"), 0644)
				err = os.Rename(filepath.Join(root, "hot"), filepath.Join(t.TempDir(), "saved"))
				if err == nil {
					err = os.Symlink(outside, filepath.Join(root, "hot"))
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			partial := indexedScan(t, st, p, false)
			if partial.Stats.SubtreeSources != 1 {
				t.Fatal(partial.Stats)
			}
			for _, en := range partial.Entries {
				if en.Path == "hot/secret.txt" {
					t.Fatal("followed replacement symlink")
				}
			}
			assertFullEquivalent(t, st, p, partial)
		})
	}
}
func TestSubtreeUntrustedProjectionAndPolicyAlwaysFallBack(t *testing.T) {
	for _, kind := range []string{"deleted-row", "modified-row", "tag", "expired", "policy", "root-event", "date", "force"} {
		t.Run(kind, func(t *testing.T) {
			st, p := subtreeFixture(t)
			switch kind {
			case "deleted-row":
				st.DB.Exec("DELETE FROM source_paths WHERE relative_path='cold/nested/0.txt'")
			case "modified-row":
				st.DB.Exec("UPDATE source_paths SET entry_data='{}' WHERE relative_path='cold/nested/0.txt'")
			case "tag":
				st.DB.Exec("UPDATE source_scans SET projection_tag='bad'")
			case "expired":
				base, _ := st.LoadSourceProjection(context.Background(), p.Sources[0].ID)
				gen, _ := st.BeginSource(context.Background(), p.Sources[0].ID)
				base.Scope.Partial = true
				base.Scope.BaseGeneration = base.Scope.Generation
				base.Scope.Generation = gen
				base.Scope.FullScanAt = time.Now().Add(-2 * time.Hour)
				if err := st.CommitSource(context.Background(), base.Scope, base.Paths); err != nil {
					t.Fatal(err)
				}
			case "policy":
				p.Rules = []backup.Rule{{Action: "exclude", Pattern: "cold/**"}}
			case "root-event":
				p.RuntimeDirtyRoots[p.Sources[0].ID] = []string{"root.txt"}
			case "date":
				days := 30
				p.FileFilters.ModifiedWithinDays = &days
			}
			result := indexedScan(t, st, p, kind == "force")
			if result.Stats.SubtreeSources != 0 || result.Stats.FullSources != 1 {
				t.Fatal("unsafe projection reused", result.Stats)
			}
			assertFullEquivalent(t, st, p, result)
		})
	}
}
func TestPartialCommitRejectsChangedGeneration(t *testing.T) {
	st, p := subtreeFixture(t)
	base, _ := st.LoadSourceProjection(context.Background(), p.Sources[0].ID)
	generation, _ := st.BeginSource(context.Background(), p.Sources[0].ID)
	indexedScan(t, st, p, true)
	scope := base.Scope
	scope.Partial = true
	scope.BaseGeneration = base.Scope.Generation
	scope.Generation = generation
	if err := st.CommitSource(context.Background(), scope, base.Paths); !errors.Is(err, backup.ErrIndexStale) {
		t.Fatalf("stale merge committed: %v", err)
	}
}
func TestCancelledSubtreeScanPreservesProjection(t *testing.T) {
	st, p := subtreeFixture(t)
	before, _ := st.LoadSourceProjection(context.Background(), p.Sources[0].ID)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := backup.ScanIndexed(ctx, p, st, false); err == nil {
		t.Fatal("cancelled scan succeeded")
	}
	after, _ := st.LoadSourceProjection(context.Background(), p.Sources[0].ID)
	if after == nil || after.Scope.Generation != before.Scope.Generation {
		t.Fatal("cancelled scan changed projection")
	}
}

type conflictingProjectionIndex struct {
	*Store
	plan       backup.Plan
	conflicted bool
}

func (i *conflictingProjectionIndex) CommitSource(ctx context.Context, scope backup.SourceScope, paths []backup.IndexedPath) error {
	if scope.Partial && !i.conflicted {
		i.conflicted = true
		if _, err := backup.ScanIndexed(ctx, i.plan, i.Store, true); err != nil {
			return err
		}
	}
	return i.Store.CommitSource(ctx, scope, paths)
}
func TestSubtreeGenerationConflictAutomaticallyRetriesFullScan(t *testing.T) {
	st, p := subtreeFixture(t)
	index := &conflictingProjectionIndex{Store: st, plan: p}
	result, err := backup.ScanIndexed(context.Background(), p, index, false)
	if err != nil {
		t.Fatal(err)
	}
	if !index.conflicted || result.Stats.FullSources != 1 || result.Stats.SubtreeSources != 0 || result.Stats.PathsVisited != 20 {
		t.Fatal("conflict did not trigger full scan", result.Stats)
	}
	assertFullEquivalent(t, st, p, result)
}
func TestSubtreeCancellationDuringWalkDoesNotPublish(t *testing.T) {
	st, p := subtreeFixture(t)
	before, _ := st.LoadSourceProjection(context.Background(), p.Sources[0].ID)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = backup.Observe(ctx, func(e backup.WorkEvent) {
		if e.Path != "" {
			cancel()
		}
	})
	if _, err := backup.ScanIndexed(ctx, p, st, false); err == nil {
		t.Fatal("cancelled subtree committed")
	}
	after, _ := st.LoadSourceProjection(context.Background(), p.Sources[0].ID)
	if after == nil || after.Scope.Generation != before.Scope.Generation {
		t.Fatal("partial generation published")
	}
}
