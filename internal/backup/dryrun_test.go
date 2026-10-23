package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDryRunNestedUnchangedFilesPreservesHistory(t *testing.T) {
	ctx := context.Background()
	p := fixture(t)
	rs := DetectRsync("")
	if !rs.Available {
		t.Skip("requires rsync")
	}
	for _, src := range p.Sources {
		write(t, filepath.Join(src.Path, "deep/nested/same.txt"), src.ID)
	}
	entries, _, err := Scan(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := Execute(ctx, p, p.Destinations[0], entries, nil, rs, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if err = DryRun(ctx, p, p.Destinations[0], entries, previous, rs); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(p.Sources[0].Path, "deep/nested/new.txt"), "new file")
	changed, _, err := Scan(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if err = DryRun(ctx, p, p.Destinations[0], changed, previous, rs); err != nil {
		t.Fatal(err)
	}
	if err = AuditSnapshot(ctx, *previous); err != nil {
		t.Fatal("dry run changed history", err)
	}
	snapshots, err := os.ReadDir(filepath.Dir(previous.Path))
	if err != nil || len(snapshots) != 1 {
		t.Fatal("preview published a snapshot", err)
	}
}

// Preview must not read historical contents merely to prepare a throwaway
// reference. Execute retains its separate reference and full verification path.
func TestDryRunDoesNotOpenHistoricalContents(t *testing.T) {
	p := fixture(t)
	write(t, filepath.Join(p.Sources[0].Path, "file.txt"), "data")
	rs := DetectRsync("")
	if !rs.Available {
		t.Skip("requires rsync")
	}
	entries, _, err := Scan(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	// An unavailable prior location proves the preflight does not access it.
	previous := &Snapshot{Path: filepath.Join(t.TempDir(), "unavailable"), Entries: entries}
	if err := DryRun(context.Background(), p, p.Destinations[0], entries, previous, rs); err != nil {
		t.Fatal(err)
	}
}
