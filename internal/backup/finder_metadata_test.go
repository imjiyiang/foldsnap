package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFinderMetadataOnlyCleanedFromUnselectedStagingFiles(t *testing.T) {
	stage := t.TempDir()
	os.Mkdir(filepath.Join(stage, "source"), 0755)
	write(t, filepath.Join(stage, ".DS_Store"), "Finder root cache")
	write(t, filepath.Join(stage, "source/.DS_Store"), "selected source data")
	write(t, filepath.Join(stage, "unexpected.txt"), "must not silently discard")
	entries := []Entry{{SourceID: "source", Path: ".DS_Store"}}
	n, err := cleanStagingFinderMetadata(context.Background(), stage, entries)
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if b, err := os.ReadFile(filepath.Join(stage, "source/.DS_Store")); err != nil || string(b) != "selected source data" {
		t.Fatal("selected metadata changed", err)
	}
	if err = VerifyTreeLayout(context.Background(), stage, entries, []Source{{ID: "source"}}, false); err == nil {
		t.Fatal("unexpected non-Finder file accepted")
	}
	os.Symlink(filepath.Join(stage, "unexpected.txt"), filepath.Join(stage, ".DS_Store"))
	if _, err = cleanStagingFinderMetadata(context.Background(), stage, entries); err == nil {
		t.Fatal("linked metadata accepted")
	}
}
func TestFinderOpeningStagingDoesNotFailBackup(t *testing.T) {
	p := fixture(t)
	write(t, filepath.Join(p.Sources[0].Path, ".DS_Store"), "intentional selected source metadata")
	entries, _, err := Scan(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(p.Destinations[0].Path, "LocalBackup", "plans", p.ID, "staging")
	injected := false
	ctx := Observe(context.Background(), func(event WorkEvent) {
		if event.Phase == "transfer" && !injected {
			dirs, e := os.ReadDir(root)
			if e != nil {
				t.Fatal(e)
			}
			for _, d := range dirs {
				if d.IsDir() {
					write(t, filepath.Join(root, d.Name(), ".DS_Store"), "Finder cache during transfer")
					injected = true
				}
			}
		}
	})
	sn, err := Execute(ctx, p, p.Destinations[0], entries, nil, DetectRsync(""), func(string) {})
	if err != nil || !injected {
		t.Fatal("Finder reproduction", err, injected)
	}
	if _, err = os.Lstat(filepath.Join(sn.Path, ".DS_Store")); !os.IsNotExist(err) {
		t.Fatal("root cache published")
	}
	if _, err = os.Stat(filepath.Join(sn.Path, p.Sources[0].ID, ".DS_Store")); err != nil {
		t.Fatal("source metadata lost", err)
	}
	if err = AuditSnapshot(context.Background(), *sn); err != nil {
		t.Fatal(err)
	}
}
