package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func interruptedStaging(t *testing.T) (Plan, []Entry, RsyncInfo, string) {
	t.Helper()
	p := fixture(t)
	rs := DetectRsync("")
	if !rs.Available {
		t.Skip("requires rsync")
	}
	for _, src := range p.Sources {
		write(t, filepath.Join(src.Path, "deep/file.txt"), strings.Repeat(src.ID, 100))
	}
	entries, _, err := Scan(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = Observe(ctx, func(e WorkEvent) {
		if e.Phase == "transfer" && e.Source == p.Sources[1].Name {
			cancel()
		}
	})
	if _, err = Execute(ctx, p, p.Destinations[0], entries, nil, rs, func(string) {}); err == nil {
		t.Fatal("interruption not exercised")
	}
	base := filepath.Join(p.Destinations[0].Path, "LocalBackup", "plans", p.ID)
	dirs, err := os.ReadDir(filepath.Join(base, "staging"))
	if err != nil {
		t.Fatal(err)
	}
	old := ""
	for _, d := range dirs {
		if d.IsDir() {
			old = filepath.Join(base, "staging", d.Name())
		}
	}
	if old == "" {
		t.Fatal("missing unfinished tree")
	}
	if _, err = os.Stat(filepath.Join(old, p.Sources[0].ID, "deep/file.txt")); err != nil {
		t.Fatal("first transfer incomplete", err)
	}
	committed, _ := os.ReadDir(filepath.Join(base, "snapshots"))
	if len(committed) != 0 {
		t.Fatal("interrupted tree published")
	}
	return p, entries, rs, old
}
func TestInterruptedStagingReusedAndPublishedIndependently(t *testing.T) {
	p, entries, rs, old := interruptedStaging(t)
	reused := []string{}
	snap, err := Execute(context.Background(), p, p.Destinations[0], entries, nil, rs, func(line string) {
		if strings.Contains(line, "恢复暂存文件") {
			reused = append(reused, line)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(reused) != 1 || !strings.Contains(reused[0], "1 个") {
		t.Fatal("completed file not recovered", reused)
	}
	if err = AuditSnapshot(context.Background(), *snap); err != nil {
		t.Fatal(err)
	}
	// Copy-on-write cloning/copy must isolate the result from later staged writes.
	oldFile := filepath.Join(old, p.Sources[0].ID, "deep/file.txt")
	if err = os.WriteFile(oldFile, []byte("tamper old interrupted data"), 0644); err != nil {
		t.Fatal(err)
	}
	if err = AuditSnapshot(context.Background(), *snap); err != nil {
		t.Fatal("snapshot shared mutable staging inode", err)
	}
	if _, err = os.Stat(receiptPath(filepath.Dir(old), snap.ID)); !os.IsNotExist(err) {
		t.Fatal("successful receipt left behind", err)
	}
}
func TestStagingRejectsCorruptChangedOrUnboundCandidates(t *testing.T) {
	for _, kind := range []string{"corrupt", "source-changed", "binding", "receipt", "symlink", "partial"} {
		t.Run(kind, func(t *testing.T) {
			p, entries, rs, old := interruptedStaging(t)
			staged := filepath.Join(old, p.Sources[0].ID, "deep/file.txt")
			switch kind {
			case "corrupt":
				st, _ := os.Stat(staged)
				os.WriteFile(staged, []byte(strings.Repeat("x", int(st.Size()))), 0644)
				os.Chtimes(staged, st.ModTime(), st.ModTime())
			case "partial":
				os.WriteFile(staged, []byte("partial"), 0644)
			case "source-changed":
				write(t, filepath.Join(p.Sources[0].Path, "deep/file.txt"), "new source data")
			case "binding":
				p.Name = "same cosmetic name"
				p.Rules = []Rule{{Action: "exclude", Pattern: "*.tmp"}}
			case "receipt":
				os.WriteFile(receiptPath(filepath.Dir(old), filepath.Base(old)), []byte("{}"), 0600)
			case "symlink":
				os.Remove(staged)
				os.Symlink(filepath.Join(p.Sources[0].Path, "deep/file.txt"), staged)
			}
			var err error
			entries, _, err = Scan(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			reused := 0
			snap, err := Execute(context.Background(), p, p.Destinations[0], entries, nil, rs, func(line string) {
				if strings.Contains(line, "恢复暂存文件") {
					reused++
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			if reused != 0 {
				t.Fatal("untrusted file reused", kind)
			}
			if err = AuditSnapshot(context.Background(), *snap); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestStagingCancellationCannotPublish(t *testing.T) {
	p, entries, rs, old := interruptedStaging(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = Observe(ctx, func(e WorkEvent) {
		if strings.HasPrefix(e.Path, old+string(os.PathSeparator)) {
			cancel()
		}
	})
	if _, err := Execute(ctx, p, p.Destinations[0], entries, nil, rs, func(string) {}); err == nil {
		t.Fatal("cancelled recovery succeeded")
	}
	dirs, _ := os.ReadDir(filepath.Join(filepath.Dir(filepath.Dir(old)), "snapshots"))
	if len(dirs) != 0 {
		t.Fatal("cancelled recovery published")
	}
}
