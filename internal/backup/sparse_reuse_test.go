package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestSparsePreflightMatchesActualAllowlistAndFailureCannotPublish(t *testing.T) {
	for _, scenario := range []string{"success", "preflight-failure", "fallback-copy"} {
		fail := scenario == "preflight-failure"
		fallback := scenario == "fallback-copy"
		t.Run(scenario, func(t *testing.T) {
			p, entries, first, rs := sparseFixture(t, "snapshot")
			oldCandidate := filepath.Join(first.Path, p.Sources[0].ID, "keep.txt")
			if fallback {
				if err := os.Chmod(oldCandidate, 0600); err != nil {
					t.Fatal(err)
				}
			}
			dir := t.TempDir()
			preflight := filepath.Join(dir, "preflight")
			actual := filepath.Join(dir, "actual")
			wrapper := filepath.Join(dir, "rsync")
			failClause := ""
			if fail {
				failClause = "if [ \"$kind\" = preflight ]; then exit 97; fi\n"
			}
			script := fmt.Sprintf("#!/bin/sh\nkind=actual\nfor arg do\n [ \"$arg\" = --dry-run ] && kind=preflight\ndone\nfor arg do\n case \"$arg\" in\n --link-dest=*) exit 93;;\n --files-from=*) cp \"${arg#--files-from=}\" %q/\"$kind\";;\n esac\ndone\n%s exec %q \"$@\"\n", dir, failClause, rs.Path)
			if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			rs.Path = wrapper
			next, err := Execute(context.Background(), p, p.Destinations[0], entries, first, rs, func(string) {})
			dry, readErr := os.ReadFile(preflight)
			want := []byte("nested/change.txt\x00")
			if fallback {
				want = []byte("keep.txt\x00nested/change.txt\x00")
			}
			if readErr != nil || !bytes.Equal(dry, want) {
				t.Fatal("preflight includes reused files or loses nested transfer", string(dry), readErr)
			}
			if fail {
				if err == nil || next != nil {
					t.Fatal("failed preflight published")
				}
				if _, err = os.Stat(actual); !os.IsNotExist(err) {
					t.Fatal("transfer ran after failed preflight")
				}
				rows, err := os.ReadDir(filepath.Dir(first.Path))
				if err != nil || len(rows) != 1 {
					t.Fatal("failed preflight committed history", err)
				}
			} else {
				if err != nil || next == nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(actual)
				if err != nil || !bytes.Equal(data, dry) {
					t.Fatal("transfer differs from freshly preflighted allowlist", err)
				}
			}
			if fallback {
				oldInfo, _ := os.Stat(oldCandidate)
				newInfo, _ := os.Stat(filepath.Join(next.Path, p.Sources[0].ID, "keep.txt"))
				if oldInfo.Mode().Perm() != 0600 || os.SameFile(oldInfo, newInfo) {
					t.Fatal("fallback copy changed historical metadata or linked wrong candidate")
				}
				if err = AuditSnapshot(context.Background(), *next); err != nil {
					t.Fatal(err)
				}
			} else if err = AuditSnapshot(context.Background(), *first); err != nil {
				t.Fatal("preflight/reuse changed history", err)
			}
			if fail {
				staging := filepath.Join(filepath.Dir(filepath.Dir(first.Path)), "staging")
				oldStages, err := os.ReadDir(staging)
				if err != nil {
					t.Fatal(err)
				}
				oldReads := 0
				ctx := Observe(context.Background(), func(e WorkEvent) {
					for _, row := range oldStages {
						if row.IsDir() && e.Path != "" && Contains(filepath.Join(staging, row.Name()), e.Path) {
							oldReads++
						}
					}
				})
				retry, err := Execute(ctx, p, p.Destinations[0], entries, first, DetectRsync(""), func(string) {})
				if err != nil || retry == nil {
					t.Fatal("retry after failed preflight", err)
				}
				a, _ := os.Stat(oldCandidate)
				b, _ := os.Stat(filepath.Join(retry.Path, p.Sources[0].ID, "keep.txt"))
				if oldReads != 0 || !os.SameFile(a, b) {
					t.Fatal("retry copied/read interrupted candidate despite reusable committed history", oldReads)
				}
				if err = AuditSnapshot(context.Background(), *retry); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestSparseReuseReadsSourceOnceAndFinalTargetOnce(t *testing.T) {
	for _, mode := range []string{"snapshot", "incremental", "mirror"} {
		t.Run(mode, func(t *testing.T) {
			p, entries, first, rs := sparseFixture(t, mode)
			counts := map[string]int{}
			ctx := Observe(context.Background(), func(e WorkEvent) {
				if e.Path != "" && !e.CacheHit {
					counts[e.Path]++
				}
			})
			next, err := Execute(ctx, p, p.Destinations[0], entries, first, rs, func(string) {})
			if err != nil || next == nil {
				t.Fatal(err)
			}
			old := filepath.Join(first.Path, p.Sources[0].ID, "keep.txt")
			wantBaseline := 0
			if mode != "snapshot" {
				wantBaseline = 1
			}
			if counts[old] != wantBaseline || counts[filepath.Join(p.Sources[0].Path, "keep.txt")] != 1 {
				t.Fatal("redundant/missing pre-link read", counts)
			}
			finalReads := 0
			for path, count := range counts {
				if filepath.Base(path) == "keep.txt" && Contains(filepath.Dir(filepath.Dir(next.Path)), path) && path != old {
					finalReads += count
				}
			}
			if finalReads != 1 {
				t.Fatal("final target full read missing or duplicated", counts)
			}
			a, _ := os.Stat(old)
			b, _ := os.Stat(filepath.Join(next.Path, p.Sources[0].ID, "keep.txt"))
			if !os.SameFile(a, b) {
				t.Fatal("candidate not directly linked")
			}
			if err = AuditSnapshot(context.Background(), *first); err != nil {
				t.Fatal(err)
			}
			if err = AuditSnapshot(context.Background(), *next); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func sparseFixture(t *testing.T, mode string) (Plan, []Entry, *Snapshot, RsyncInfo) {
	t.Helper()
	p := fixture(t)
	p.Mode = mode
	p.Sources = p.Sources[:1]
	rs := DetectRsync("")
	if !rs.Available {
		t.Skip("requires rsync")
	}
	write(t, filepath.Join(p.Sources[0].Path, "keep.txt"), "AAAA")
	write(t, filepath.Join(p.Sources[0].Path, "nested/change.txt"), "old")
	entries, _, err := Scan(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	first, err := Execute(context.Background(), p, p.Destinations[0], entries, nil, rs, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(p.Sources[0].Path, "nested/change.txt"), "new content")
	entries, _, err = Scan(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	return p, entries, first, rs
}

func TestSparseReuseCorruptionReplacementAndCancellationNeverPublish(t *testing.T) {
	for _, mode := range []string{"snapshot", "incremental", "mirror"} {
		for _, attack := range []string{"existing-corruption", "during-source-check", "candidate-replaced", "before-final-content", "before-final-mode", "source-corruption", "cancel"} {
			t.Run(mode+"/"+attack, func(t *testing.T) {
				p, entries, first, rs := sparseFixture(t, mode)
				src := filepath.Join(p.Sources[0].Path, "keep.txt")
				old := filepath.Join(first.Path, p.Sources[0].ID, "keep.txt")
				corrupt := func(path string) {
					st, err := os.Stat(path)
					if err != nil {
						t.Fatal(err)
					}
					write(t, path, "BBBB")
					if err = os.Chtimes(path, st.ModTime(), st.ModTime()); err != nil {
						t.Fatal(err)
					}
				}
				if attack == "existing-corruption" {
					corrupt(old)
				}
				if attack == "source-corruption" {
					corrupt(src)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				triggered := false
				ctx = Observe(ctx, func(e WorkEvent) {
					if triggered {
						return
					}
					if e.Path == src && (attack == "during-source-check" || attack == "candidate-replaced") {
						triggered = true
						if attack == "during-source-check" {
							corrupt(old)
						} else {
							outside := filepath.Join(t.TempDir(), "outside")
							write(t, outside, "AAAA")
							if err := os.Remove(old); err != nil {
								t.Fatal(err)
							}
							if err := os.Symlink(outside, old); err != nil {
								t.Fatal(err)
							}
						}
					}
					if e.Phase == "verify" && (attack == "before-final-content" || attack == "before-final-mode" || attack == "cancel") {
						triggered = true
						if attack == "cancel" {
							cancel()
							return
						}
						root := filepath.Join(filepath.Dir(filepath.Dir(first.Path)), "staging")
						rows, err := os.ReadDir(root)
						if err != nil {
							t.Fatal(err)
						}
						found := false
						for _, row := range rows {
							if row.IsDir() {
								found = true
								path := filepath.Join(root, row.Name(), p.Sources[0].ID, "keep.txt")
								if attack == "before-final-content" {
									corrupt(path)
								} else if err = os.Chmod(path, 0600); err != nil {
									t.Fatal(err)
								}
							}
						}
						if !found {
							t.Fatal("missing staging")
						}
					}
				})
				next, err := Execute(ctx, p, p.Destinations[0], entries, first, rs, func(string) {})
				if err == nil || next != nil {
					t.Fatal("unsafe candidate published", attack)
				}
				if attack != "existing-corruption" && attack != "source-corruption" && !triggered {
					t.Fatal("attack not exercised")
				}
				if attack == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				rows, err := os.ReadDir(filepath.Dir(first.Path))
				if err != nil || len(rows) != 1 {
					t.Fatal("failed attempt published history", err)
				}
				if mode != "snapshot" {
					current, err := ReadCurrent(p.Destinations[0], p.ID)
					if err != nil || current == nil || current.ID != first.ID {
						t.Fatal("failed attempt advanced current", err)
					}
				}
			})
		}
	}
}
