package backup

import (
	"fmt"
	"math/rand"
	"testing"
	"time"
)

func TestWalkSelectionMatchesUncachedPolicy(t *testing.T) {
	at := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	min, max, days := int64(2), int64(200), 10
	paths := []string{"top.txt", "cache", "cache/keep.txt", "cache/drop.txt", "cache/deep/keep.go", ".hidden", ".hidden/deep/file.txt", "src", "src/include", "src/include/file.go", "src/include/nested/file.txt", "src/other.txt", "node_modules/a/b.js", "other/node_modules/a/b.js", "other/.Trash/file", "数据/子目录/测试.go"}
	for i := 0; i < 300; i++ {
		paths = append(paths, fmt.Sprintf("tree/%d/nested/%d/file.go", i%17, i))
	}
	policies := []Plan{
		{},
		{Rules: []Rule{{"exclude", "cache/**"}, {"include", "cache/keep.txt"}}, FileFilters: FileFilters{Exceptions: []FileException{{"cache/keep.txt", "include"}, {"cache/deep/keep.go", "include"}, {"top.txt", "exclude"}}}},
		{Rules: []Rule{{"include", "src/include/**"}, {"exclude", "src/**"}, {"exclude", "**/node_modules/**"}, {"exclude", "**/.Trash/**"}}},
		{Rules: []Rule{{"exclude", "*.txt"}, {"include", "*.go"}}, FileFilters: FileFilters{MinBytes: &min, MaxBytes: &max, ModifiedWithinDays: &days, Types: []string{"code"}, Exceptions: []FileException{{"cache/keep.txt", "include"}}}},
	}
	for pi, p := range policies {
		for _, hidden := range []bool{false, true} {
			s, err := CompileSelection(p, Source{ExcludeHidden: hidden, Rules: []Rule{{"exclude", "other/**"}}})
			if err != nil {
				t.Fatal(err)
			}
			w := newWalkSelection(s)
			// Out-of-order traversal and revisits exercise ancestor reconstruction,
			// not just the directory-first happy path.
			order := append([]string(nil), paths...)
			rand.New(rand.NewSource(7)).Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
			for _, path := range order {
				for _, kind := range []string{"file", "directory", "symlink"} {
					for _, size := range []int64{0, 5, 300} {
						for _, mtime := range []time.Time{at, at.Add(-30 * 24 * time.Hour)} {
							want, reason := s.Match(path, kind, size, mtime)
							got, gotReason := w.Match(path, kind, size, mtime)
							if got != want || gotReason != reason {
								t.Fatalf("policy %d hidden=%v %s %s: got (%v,%q), want (%v,%q)", pi, hidden, path, kind, got, gotReason, want, reason)
							}
						}
					}
				}
			}
		}
	}
}

func TestWalkSelectionBoundedAndFreshPolicy(t *testing.T) {
	s, err := CompileSelection(Plan{Rules: []Rule{{"exclude", "blocked/**"}}, FileFilters: FileFilters{Exceptions: []FileException{{"blocked/keep.txt", "include"}}}}, Source{})
	if err != nil {
		t.Fatal(err)
	}
	w := newWalkSelection(s)
	for i := 0; i < walkSelectionLimit+100; i++ {
		path := fmt.Sprintf("many/%d/deep/file", i)
		want, reason := s.Match(path, "file", 0, time.Time{})
		got, gotReason := w.Match(path, "file", 0, time.Time{})
		if got != want || gotReason != reason || len(w.parents) > walkSelectionLimit {
			t.Fatal("eviction changed policy or exceeded bound")
		}
	}
	for _, path := range []string{"blocked/file", "blocked/deep/file", "blocked/keep.txt"} {
		want, reason := s.Match(path, "file", 0, time.Time{})
		got, gotReason := w.Match(path, "file", 0, time.Time{})
		if got != want || gotReason != reason {
			t.Fatal("excluded ancestor lost after eviction", path)
		}
	}
	changed, err := CompileSelection(Plan{Rules: []Rule{{"include", "blocked/**"}}}, Source{})
	if err != nil {
		t.Fatal(err)
	}
	fresh := newWalkSelection(changed)
	if ok, _ := fresh.Match("blocked/deep/file", "file", 0, time.Time{}); !ok {
		t.Fatal("old policy leaked into new scan")
	}
}

func BenchmarkWalkSelection(b *testing.B) {
	s, err := CompileSelection(Plan{Rules: []Rule{{"exclude", ".DS_Store"}, {"exclude", "**/node_modules/**"}, {"exclude", "**/.Trash/**"}}}, Source{})
	if err != nil {
		b.Fatal(err)
	}
	paths := make([]string, 4096)
	for i := range paths {
		paths[i] = fmt.Sprintf("models/library/python/site-packages/module%d/submodule/data/file%d.py", i/64, i)
	}
	for _, cached := range []bool{false, true} {
		b.Run(fmt.Sprintf("parent-reuse=%v", cached), func(b *testing.B) {
			w := newWalkSelection(s)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				p := paths[i%len(paths)]
				if cached {
					w.Match(p, "file", 1, time.Time{})
				} else {
					s.Match(p, "file", 1, time.Time{})
				}
			}
		})
	}
}
