package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestUnchangedExecutionKeepsRestorePointAndRepositoryTree(t *testing.T) {
	for _, mode := range []string{"snapshot", "incremental", "mirror"} {
		t.Run(mode, func(t *testing.T) {
			p := fixture(t)
			p.Mode = mode
			rs := DetectRsync("")
			if !rs.Available {
				t.Skip("requires rsync")
			}
			write(t, filepath.Join(p.Sources[0].Path, "nested", "file.txt"), "unchanged payload")
			if err := os.Symlink("nested/file.txt", filepath.Join(p.Sources[0].Path, "link")); err != nil {
				t.Fatal(err)
			}
			entries, _, err := Scan(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			first, err := Execute(context.Background(), p, p.Destinations[0], entries, nil, rs, func(string) {})
			if err != nil {
				t.Fatal(err)
			}
			_, root, err := OpenRepository(p.Destinations[0], false)
			if err != nil {
				t.Fatal(err)
			}
			before := repositoryShape(t, root)
			var read int64
			ctx := Observe(context.Background(), func(e WorkEvent) { read += e.Bytes })
			next, err := Execute(ctx, p, p.Destinations[0], entries, first, rs, func(string) {})
			if err != nil || next != nil {
				t.Fatalf("unchanged execution created snapshot: %v %v", next, err)
			}
			if read < 2*int64(len("unchanged payload")) {
				t.Fatal("source and baseline were not fully read", read)
			}
			if after := repositoryShape(t, root); !bytes.Equal(before, after) {
				t.Fatal("unchanged run wrote repository tree or historical metadata")
			}
		})
	}
}

func repositoryShape(t *testing.T, root string) []byte {
	t.Helper()
	rows := []any{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		st, err := os.Lstat(path)
		if err != nil {
			return err
		}
		en := Entry{}
		populateIdentity(&en, st, "")
		// Atime is an OS read-side effect, outside application writes.
		rows = append(rows, []any{strings.TrimPrefix(path, root), st.Mode(), st.Size(), st.ModTime().UnixNano(), en.Identity, en.ChangeTime, en.BirthTime})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestUnchangedExecutionRejectsCorruptionChangesAndCancellation(t *testing.T) {
	cases := []string{"baseline-content", "baseline-extra-path", "baseline-manifest", "source-after-scan", "late-source-addition", "late-earlier-source-change", "late-baseline-change", "late-baseline-manifest", "cancel"}
	for _, kind := range cases {
		t.Run(kind, func(t *testing.T) {
			p := fixture(t)
			rs := DetectRsync("")
			if !rs.Available {
				t.Skip("requires rsync")
			}
			src := p.Sources[0].Path
			write(t, filepath.Join(src, "a.txt"), "AAA")
			write(t, filepath.Join(src, "z.txt"), "ZZZ")
			entries, _, err := Scan(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			first, err := Execute(context.Background(), p, p.Destinations[0], entries, nil, rs, func(string) {})
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(first.Path, p.Sources[0].ID, "a.txt")
			switch kind {
			case "baseline-content":
				write(t, target, "BAD")
			case "baseline-extra-path":
				write(t, filepath.Join(first.Path, "untracked.txt"), "bad")
			case "baseline-manifest":
				raw, err := os.ReadFile(filepath.Join(first.Path, "manifest.json"))
				if err != nil {
					t.Fatal(err)
				}
				var changed Snapshot
				if err = json.Unmarshal(raw, &changed); err != nil {
					t.Fatal(err)
				}
				changed.PlanName = "tampered"
				raw, _ = json.Marshal(changed)
				if err = os.WriteFile(filepath.Join(first.Path, "manifest.json"), raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "source-after-scan":
				write(t, filepath.Join(src, "a.txt"), "BAD")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			triggered := false
			ctx = Observe(ctx, func(e WorkEvent) {
				if triggered || e.Path != filepath.Join(src, "z.txt") {
					return
				}
				triggered = true
				switch kind {
				case "late-source-addition":
					write(t, filepath.Join(src, "new.txt"), "new")
				case "late-earlier-source-change":
					info, err := os.Stat(filepath.Join(src, "a.txt"))
					if err != nil {
						t.Fatal(err)
					}
					write(t, filepath.Join(src, "a.txt"), "BBB")
					if err = os.Chtimes(filepath.Join(src, "a.txt"), info.ModTime(), info.ModTime()); err != nil {
						t.Fatal(err)
					}
				case "late-baseline-change":
					write(t, target, "BAD")
				case "late-baseline-manifest":
					changed := *first
					changed.PlanName = "tampered during verification"
					data, _ := json.Marshal(changed)
					if err := os.WriteFile(filepath.Join(first.Path, "manifest.json"), data, 0600); err != nil {
						t.Fatal(err)
					}
				case "cancel":
					cancel()
				}
			})
			next, err := Execute(ctx, p, p.Destinations[0], entries, first, rs, func(string) {})
			if err == nil || next != nil {
				t.Fatalf("unsafe unchanged execution succeeded: %v %v", next, err)
			}
			if kind == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			rows, err := os.ReadDir(filepath.Dir(first.Path))
			if err != nil || len(rows) != 1 {
				t.Fatal("failed verification published generation", err)
			}
			staging := filepath.Join(filepath.Dir(filepath.Dir(first.Path)), "staging")
			rows, err = os.ReadDir(staging)
			if err != nil || len(rows) != 0 {
				t.Fatal("failed unchanged verification created staging writes", err)
			}
		})
	}
}

func TestDirectReuseExcludesSharedInodesFromRsync(t *testing.T) {
	p := fixture(t)
	rs := DetectRsync("")
	if !rs.Available {
		t.Skip("requires rsync")
	}
	src := p.Sources[0].Path
	for _, name := range []string{"same.txt", "mode.txt", "content.txt", "old-name.txt"} {
		write(t, filepath.Join(src, name), "original")
	}
	entries, _, err := Scan(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	first, err := Execute(context.Background(), p, p.Destinations[0], entries, nil, rs, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(first.Path, p.Sources[0].ID)
	oldMode, err := os.Stat(filepath.Join(oldPath, "mode.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(filepath.Join(src, "mode.txt"), 0600); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(src, "content.txt"), "new contents")
	if err = os.Rename(filepath.Join(src, "old-name.txt"), filepath.Join(src, "new-name.txt")); err != nil {
		t.Fatal(err)
	}
	nextEntries, _, err := Scan(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	list := filepath.Join(dir, "files.list")
	wrapper := filepath.Join(dir, "rsync")
	script := fmt.Sprintf("#!/bin/sh\nfor arg do\n case \"$arg\" in\n --link-dest=*) exit 93;;\n --files-from=*) cp \"${arg#--files-from=}\" %q;;\n esac\ndone\nexec %q \"$@\"\n", list, rs.Path)
	if err = os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	rs.Path = wrapper
	next, err := Execute(context.Background(), p, p.Destinations[0], nextEntries, first, rs, func(string) {})
	if err != nil || next == nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(list)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, s := range strings.Split(string(raw), "\x00") {
		names[s] = true
	}
	if names["same.txt"] || names["new-name.txt"] || !names["mode.txt"] || !names["content.txt"] {
		t.Fatal("rsync allowlist exposed reused inode or lost changed files", names)
	}
	for _, pair := range [][2]string{{"same.txt", "same.txt"}, {"old-name.txt", "new-name.txt"}} {
		a, _ := os.Stat(filepath.Join(oldPath, pair[0]))
		b, _ := os.Stat(filepath.Join(next.Path, p.Sources[0].ID, pair[1]))
		if !os.SameFile(a, b) {
			t.Fatal("unchanged/moved file was not directly hardlinked")
		}
	}
	a, _ := os.Stat(filepath.Join(oldPath, "mode.txt"))
	b, _ := os.Stat(filepath.Join(next.Path, p.Sources[0].ID, "mode.txt"))
	if a.Mode() != oldMode.Mode() || b.Mode().Perm() != 0600 || os.SameFile(a, b) {
		t.Fatal("mode-only change mutated historical inode")
	}
	if err = Verify(context.Background(), first.Path, first.Entries); err != nil {
		t.Fatal("previous snapshot damaged", err)
	}
	if err = Verify(context.Background(), next.Path, next.Entries); err != nil {
		t.Fatal(err)
	}
}

func TestUnchangedModernMetadataUpgradeRequiresGeneration(t *testing.T) {
	p := fixture(t)
	write(t, filepath.Join(p.Sources[0].Path, "file"), "payload")
	entries, _, err := Scan(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	prior := Snapshot{Mode: p.Mode, ExtendedMetadata: true, PlanConfig: &p, Sources: p.Sources, Entries: append([]Entry(nil), entries...)}
	prior.Entries[0].BasicMetadata = true
	if err = unix.Lsetxattr(filepath.Join(p.Sources[0].Path, "file"), "user.localbackup", []byte("new"), 0); err != nil {
		t.Skip(err)
	}
	view := BuildView(p, entries, &prior)
	same, err := unchangedView(p, p.Destinations[0], view, &prior, true)
	if err != nil || same {
		t.Fatal("basic metadata was promoted without publication", err)
	}
}
