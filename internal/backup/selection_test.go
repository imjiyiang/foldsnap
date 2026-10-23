package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func i64(v int64) *int64 { return &v }
func integer(v int) *int { return &v }
func TestFileConditionsBoundariesAndOverrides(t *testing.T) {
	at := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	p := Plan{EvaluatedAt: at, FileFilters: FileFilters{MinBytes: i64(10), MaxBytes: i64(20), ModifiedWithinDays: integer(7), Types: []string{"image"}}}
	src := Source{}
	selector, err := CompileSelection(p, src)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path, kind string
		size       int64
		mtime      time.Time
		want       bool
	}{
		{"a.JPG", "file", 10, at, true}, {"a.heic", "file", 20, at.Add(-7 * 24 * time.Hour), true}, {"a.png", "file", 9, at, false}, {"a.png", "file", 21, at, false}, {"a.png", "file", 10, at.Add(-7*24*time.Hour - time.Nanosecond), false}, {"a.txt", "file", 10, at, false}, {"folder", "directory", 0, at, true}, {"link.txt", "symlink", 1, at.Add(-30 * 24 * time.Hour), true},
	} {
		got, reason := selector.Match(tc.path, tc.kind, tc.size, tc.mtime)
		if got != tc.want {
			t.Errorf("%+v => %v %s", tc, got, reason)
		}
	}
	src.FileFilters = &FileFilters{}
	selector, err = CompileSelection(p, src)
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := selector.Match("a.txt", "file", 1, at.Add(-30*24*time.Hour)); !ok {
		t.Fatal("source override should replace plan file conditions")
	}
	for _, f := range []FileFilters{{MinBytes: i64(-1)}, {MinBytes: i64(20), MaxBytes: i64(10)}, {ModifiedWithinDays: integer(0)}, {ModifiedWithinDays: integer(36501)}, {Types: []string{"unknown"}}, {Exceptions: []FileException{{"../escape", "include"}}}, {Exceptions: []FileException{{"a/../b", "include"}}}, {Exceptions: []FileException{{"a", "include"}, {"a", "exclude"}}}} {
		if ValidateFileFilters(f) == nil {
			t.Fatalf("accepted invalid policy %+v", f)
		}
	}
}
func TestExactFileExceptionsTraverseOnlyRequiredParents(t *testing.T) {
	p := fixture(t)
	src := p.Sources[0]
	p.FileFilters = FileFilters{Types: []string{"image"}, MaxBytes: i64(1), Exceptions: []FileException{{"cache/deep/keep.txt", "include"}, {".hidden/secret.txt", "include"}, {"reject.png", "exclude"}}}
	p.Rules = []Rule{{"exclude", "cache/**"}}
	p.Sources[0].ExcludeHidden = true
	write(t, filepath.Join(src.Path, "cache/deep/keep.txt"), "required")
	write(t, filepath.Join(src.Path, "cache/deep/other.txt"), "not selected")
	write(t, filepath.Join(src.Path, "cache/peer.png"), "x")
	write(t, filepath.Join(src.Path, ".hidden/secret.txt"), "required hidden")
	write(t, filepath.Join(src.Path, ".hidden/peer.png"), "x")
	write(t, filepath.Join(src.Path, "reject.png"), "x")
	write(t, filepath.Join(src.Path, "normal.png"), "x")
	entries, ignored, err := Scan(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range entries {
		got[e.Path] = true
	}
	for _, name := range []string{"cache", "cache/deep", "cache/deep/keep.txt", ".hidden", ".hidden/secret.txt", "normal.png"} {
		if !got[name] {
			t.Errorf("missing %s", name)
		}
	}
	for _, name := range []string{"cache/deep/other.txt", "cache/peer.png", ".hidden/peer.png", "reject.png"} {
		if got[name] {
			t.Errorf("leaked %s", name)
		}
	}
	if len(ignored) != 4 {
		t.Fatalf("missing exclusion reasons: %+v", ignored)
	}
	rs := DetectRsync("")
	if rs.Available {
		snap, err := Execute(context.Background(), p, p.Destinations[0], entries, nil, rs, func(string) {})
		if err != nil {
			t.Fatal(err)
		}
		if err = AuditSnapshot(context.Background(), *snap); err != nil {
			t.Fatal(err)
		}
	}
}
func TestMirrorProtectsDynamicExclusionsUsingLiveMetadata(t *testing.T) {
	p := fixture(t)
	p.Mode = "mirror"
	src := p.Sources[0]
	at := time.Now().UTC()
	p.EvaluatedAt = at
	for _, name := range []string{"grown.txt", "aged.txt", "missing.txt", "deleted.txt"} {
		write(t, filepath.Join(src.Path, name), "old")
	}
	before, _, err := Scan(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	for i := range before {
		if before[i].Path == "missing.txt" {
			before[i].ModTime = at.Add(-30 * 24 * time.Hour).UnixNano()
		}
	}
	old := Snapshot{Entries: before, Sources: p.Sources}
	write(t, filepath.Join(src.Path, "grown.txt"), strings.Repeat("x", 100))
	oldDate := at.Add(-30 * 24 * time.Hour)
	os.Chtimes(filepath.Join(src.Path, "aged.txt"), oldDate, oldDate)
	os.Remove(filepath.Join(src.Path, "missing.txt"))
	os.Remove(filepath.Join(src.Path, "deleted.txt"))
	p.FileFilters = FileFilters{MaxBytes: i64(20), ModifiedWithinDays: integer(7)}
	selected, _, err := Scan(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	v := BuildView(p, selected, &old)
	kept := map[string]bool{}
	for _, e := range v.Retained {
		kept[e.Path] = true
	}
	if !kept["grown.txt"] || !kept["aged.txt"] || !kept["missing.txt"] || kept["deleted.txt"] {
		t.Fatalf("wrong protected scope: %+v", kept)
	}
	if len(v.Removed) != 1 || v.Removed[0].Path != "deleted.txt" {
		t.Fatalf("filtered files misclassified as deletion: %+v", v.Removed)
	}
}
func TestExceptionsCannotFollowLinksOrIncludeServiceData(t *testing.T) {
	p := fixture(t)
	src := p.Sources[0]
	outside := t.TempDir()
	write(t, filepath.Join(outside, "private.txt"), "outside")
	if err := os.Symlink(outside, filepath.Join(src.Path, "jump")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(src.Path, "service/token"), "private runtime data")
	p.RuntimeExcludes = []string{filepath.Join(src.Path, "service")}
	p.FileFilters.Exceptions = []FileException{{"jump/private.txt", "include"}, {"service/token", "include"}}
	entries, _, err := Scan(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Path, "jump/") || strings.HasPrefix(e.Path, "service") {
			t.Fatal("exception crossed protected boundary", e)
		}
	}
	p.FileFilters.Exceptions = []FileException{{"service", "include"}}
	p.RuntimeExcludes = nil
	if _, _, err = Scan(context.Background(), p); err == nil {
		t.Fatal("single file exception accepted directory")
	}
}
func TestMirrorRetainsParentsOfFilteredMissingFiles(t *testing.T) {
	p := fixture(t)
	src := p.Sources[0]
	write(t, filepath.Join(src.Path, "gone/nested/kept.txt"), "recoverable")
	rs := DetectRsync("")
	if !rs.Available {
		t.Skip("requires rsync")
	}
	entries, _, err := Scan(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	first, err := Execute(context.Background(), p, p.Destinations[0], entries, nil, rs, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(filepath.Join(src.Path, "gone")); err != nil {
		t.Fatal(err)
	}
	p.Mode = "mirror"
	p.FileFilters.Types = []string{"image"}
	selected, _, err := Scan(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	view := BuildView(p, selected, first)
	if len(view.Removed) != 0 || len(view.Retained) != 3 {
		t.Fatalf("protected file lost its parent structure: %+v", view)
	}
	second, err := Execute(context.Background(), p, p.Destinations[0], selected, first, rs, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if err = AuditSnapshot(context.Background(), *second); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(second.Path, src.ID, "gone/nested/kept.txt"))
	if err != nil || string(data) != "recoverable" {
		t.Fatal("protected content lost", err)
	}
}
func TestMirrorRejectsUnreviewedFilterScopeDrift(t *testing.T) {
	p := fixture(t)
	src := p.Sources[0]
	write(t, filepath.Join(src.Path, "grows.txt"), "old")
	write(t, filepath.Join(src.Path, "deleted.txt"), "delete me")
	rs := DetectRsync("")
	if !rs.Available {
		t.Skip("requires rsync")
	}
	entries, _, err := Scan(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	first, err := Execute(context.Background(), p, p.Destinations[0], entries, nil, rs, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	p.Mode = "mirror"
	p.FileFilters.MaxBytes = i64(20)
	write(t, filepath.Join(src.Path, "grows.txt"), strings.Repeat("x", 100))
	os.Remove(filepath.Join(src.Path, "deleted.txt"))
	selected, _, err := Scan(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	view := BuildView(p, selected, first)
	if len(view.Removed) != 1 {
		t.Fatal("fixture has wrong deletion preview")
	}
	p.ExpectedViewDigests = map[string]string{p.Destinations[0].ID: ViewDigest(view)}
	p.MirrorDeletesAuthorized = true
	// A selected old path re-enters the live filter scope after the reviewed scan.
	write(t, filepath.Join(src.Path, "grows.txt"), "new")
	if _, err = Execute(context.Background(), p, p.Destinations[0], selected, first, rs, func(string) {}); !errors.Is(err, ErrViewChanged) {
		t.Fatal("unreviewed removal accepted", err)
	}
	if err = AuditSnapshot(context.Background(), *first); err != nil {
		t.Fatal("scope drift damaged history", err)
	}
}
func TestIgnoredFileResultsAreNotTruncated(t *testing.T) {
	p := fixture(t)
	p.FileFilters.MaxBytes = i64(0)
	for i := 0; i < 2005; i++ {
		write(t, filepath.Join(p.Sources[0].Path, fmt.Sprintf("ignored-%04d.txt", i)), "x")
	}
	entries, ignored, err := Scan(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 || len(ignored) != 2005 {
		t.Fatalf("truncated exclusions: selected=%d ignored=%d", len(entries), len(ignored))
	}
}
