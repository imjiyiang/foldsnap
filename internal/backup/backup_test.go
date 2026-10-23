package backup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if e := os.MkdirAll(filepath.Dir(path), 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(path, []byte(content), 0644); e != nil {
		t.Fatal(e)
	}
}
func fixture(t *testing.T) Plan {
	t.Helper()
	base := t.TempDir()
	a, b, d := filepath.Join(base, "docs"), filepath.Join(base, "code"), filepath.Join(base, "disk")
	for _, p := range []string{a, b, d} {
		if e := os.Mkdir(p, 0755); e != nil {
			t.Fatal(e)
		}
	}
	p := Plan{ID: "plan-test", Name: "Test", Mode: "snapshot", Enabled: true, Sources: []Source{{ID: "src-docs", Name: "Docs", Path: a, TargetPath: "Docs", Enabled: true}, {ID: "src-code", Name: "Code", Path: b, TargetPath: "Code", Enabled: true}}, Destinations: []Destination{{ID: "dst-test", Name: "Disk", Path: d, Enabled: true}}, Retention: Retention{KeepLast: 2}}
	if e := Validate(&p); e != nil {
		t.Fatal(e)
	}
	return p
}
func TestSnapshotsRestoreAndMove(t *testing.T) {
	ctx := context.Background()
	p := fixture(t)
	rs := DetectRsync("")
	if !rs.Available {
		t.Skip("requires rsync")
	}
	a, b := p.Sources[0].Path, p.Sources[1].Path
	write(t, filepath.Join(a, "same.txt"), "documents")
	write(t, filepath.Join(b, "same.txt"), "code")
	write(t, filepath.Join(a, "to-move.txt"), "unchanged large contents")
	write(t, filepath.Join(a, "deleted.txt"), "keep in history")
	write(t, filepath.Join(a, "node_modules/lib/cache"), "cache")
	write(t, filepath.Join(a, "empty/.hidden"), "hidden")
	if e := os.Symlink("same.txt", filepath.Join(a, "link")); e != nil {
		t.Fatal(e)
	}
	p.Sources[0].Rules = []Rule{{"exclude", "**/node_modules/**"}}
	entries, ignored, e := Scan(ctx, p)
	if e != nil {
		t.Fatal(e)
	}
	if len(ignored) == 0 {
		t.Fatal("rules did not exclude cache")
	}
	first, e := Execute(ctx, p, p.Destinations[0], entries, nil, rs, func(s string) { t.Log(s) })
	if e != nil {
		t.Fatal(e)
	}
	if e = Verify(ctx, first.Path, entries); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(first.Path, "src-docs/node_modules/lib/cache")); !os.IsNotExist(e) {
		t.Fatal("excluded file copied")
	}
	if data, _ := os.ReadFile(filepath.Join(first.Path, "src-code/same.txt")); string(data) != "code" {
		t.Fatal("source collision")
	}
	if e = os.Rename(filepath.Join(a, "to-move.txt"), filepath.Join(a, "renamed.txt")); e != nil {
		t.Fatal(e)
	}
	os.Remove(filepath.Join(a, "deleted.txt"))
	write(t, filepath.Join(a, "same.txt"), "DOCS-NEW!")
	now := time.Now().Add(2 * time.Second)
	os.Chtimes(filepath.Join(a, "same.txt"), now, now)
	next, _, e := Scan(ctx, p)
	if e != nil {
		t.Fatal(e)
	}
	preview := Compare(next, entries, "dst-test")
	if preview.Moved != 1 || preview.Deleted != 1 || preview.Modified != 1 {
		t.Fatalf("bad preview: %+v", preview)
	}
	second, e := Execute(ctx, p, p.Destinations[0], next, first, rs, func(s string) { t.Log(s) })
	if e != nil {
		t.Fatal(e)
	}
	if e = Verify(ctx, first.Path, entries); e != nil {
		t.Fatalf("old snapshot corrupted: %v", e)
	}
	if _, e = os.Stat(filepath.Join(second.Path, "src-docs/deleted.txt")); !os.IsNotExist(e) {
		t.Fatal("deleted file present in new snapshot")
	}
	st1, _ := os.Stat(filepath.Join(first.Path, "src-docs/to-move.txt"))
	st2, _ := os.Stat(filepath.Join(second.Path, "src-docs/renamed.txt"))
	if !os.SameFile(st1, st2) {
		t.Fatal("verified move was not reused")
	}
	restoreRoot := t.TempDir()
	dest, e := Restore(ctx, *first, RestoreRequest{SourceID: "src-docs", Path: "same.txt", Destination: restoreRoot}, []Plan{p}, rs)
	if e != nil {
		t.Fatal(e)
	}
	if data, _ := os.ReadFile(filepath.Join(dest, "same.txt")); string(data) != "documents" {
		t.Fatal("incorrect restored contents")
	}
	dest, e = Restore(ctx, *first, RestoreRequest{SourceID: "src-docs", Destination: restoreRoot}, []Plan{p}, rs)
	if e != nil {
		t.Fatal(e)
	}
	if data, _ := os.ReadFile(filepath.Join(dest, "deleted.txt")); string(data) != "keep in history" {
		t.Fatal("whole directory restore failed")
	}
}
func TestFailedAndCancelledNotPublished(t *testing.T) {
	p := fixture(t)
	write(t, filepath.Join(p.Sources[0].Path, "file"), "before")
	entries, _, e := Scan(context.Background(), p)
	if e != nil {
		t.Fatal(e)
	}
	rs := DetectRsync("")
	if !rs.Available {
		t.Skip("requires rsync")
	}
	write(t, filepath.Join(p.Sources[0].Path, "file"), "changed after scan")
	if _, e = Execute(context.Background(), p, p.Destinations[0], entries, nil, rs, func(s string) { t.Log(s) }); e == nil {
		t.Fatal("published unverified contents")
	}
	root, _ := CheckDestination(p.Destinations[0])
	snaps, _ := os.ReadDir(filepath.Join(root, "plans", p.ID, "snapshots"))
	if len(snaps) != 0 {
		t.Fatal("incomplete snapshot published")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = Execute(ctx, p, p.Destinations[0], entries, nil, rs, func(s string) { t.Log(s) }); e == nil {
		t.Fatal("cancelled backup succeeded")
	}
}
func TestUnsafePathsAndVolume(t *testing.T) {
	p := fixture(t)
	orig := p.Destinations[0]
	p.Destinations[0].Path = filepath.Join(p.Sources[0].Path, "child")
	os.Mkdir(p.Destinations[0].Path, 0755)
	if e := Validate(&p); e == nil {
		t.Fatal("accepted recursive backup")
	}
	p.Destinations[0] = orig
	p.Sources[1].Path = filepath.Join(p.Sources[0].Path, "child")
	if e := Validate(&p); e == nil {
		t.Fatal("accepted overlapping sources")
	}
	d := orig
	d.VolumeID = "different-disk"
	if _, e := CheckDestination(d); e == nil {
		t.Fatal("accepted wrong volume")
	}
	for _, s := range []string{"../escape", "/etc/passwd", ".", ".."} {
		if SafeRelative(s) {
			t.Fatalf("unsafe path %s", s)
		}
	}
	root := t.TempDir()
	outside := t.TempDir()
	os.Symlink(outside, filepath.Join(root, "escape"))
	if _, e := ResolveWithin(root, "escape"); e == nil {
		t.Fatal("accepted escaping symlink")
	}
}
func TestMoveAndModifyNotReused(t *testing.T) {
	p := fixture(t)
	a := filepath.Join(p.Sources[0].Path, "a")
	write(t, a, "original")
	old, _, _ := Scan(context.Background(), p)
	b := filepath.Join(p.Sources[0].Path, "b")
	os.Rename(a, b)
	write(t, b, "modified")
	next, _, _ := Scan(context.Background(), p)
	v := Compare(next, old, "dest")
	if v.Moved != 0 || v.Added != 1 || v.Deleted != 1 {
		t.Fatalf("unsafe move detection %+v", v)
	}
}
func TestRules(t *testing.T) {
	r, e := CompileRules([]Rule{{"include", "keep.tmp"}, {"exclude", "**/*.tmp"}, {"exclude", "**/node_modules/**"}})
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		path string
		want bool
	}{{"keep.tmp", true}, {"nested/a.tmp", false}, {"node_modules/lib/a", false}, {"sub/node_modules/a", false}, {"hello.txt", true}} {
		got, _ := MatchRules(tc.path, false, r)
		if got != tc.want {
			t.Errorf("%s: %v", tc.path, got)
		}
	}
}

func TestMetadataChangeDoesNotMutateHistory(t *testing.T) {
	p := fixture(t)
	path := filepath.Join(p.Sources[0].Path, "permissions.txt")
	write(t, path, "same contents")
	rs := DetectRsync("")
	ctx := context.Background()
	old, _, e := Scan(ctx, p)
	if e != nil {
		t.Fatal(e)
	}
	first, e := Execute(ctx, p, p.Destinations[0], old, nil, rs, func(string) {})
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(path, 0600); e != nil {
		t.Fatal(e)
	}
	next, _, e := Scan(ctx, p)
	if e != nil {
		t.Fatal(e)
	}
	second, e := Execute(ctx, p, p.Destinations[0], next, first, rs, func(string) {})
	if e != nil {
		t.Fatal(e)
	}
	a, _ := os.Stat(filepath.Join(first.Path, p.Sources[0].ID, "permissions.txt"))
	b, _ := os.Stat(filepath.Join(second.Path, p.Sources[0].ID, "permissions.txt"))
	if a.Mode().Perm() != 0644 || b.Mode().Perm() != 0600 {
		t.Fatalf("permissions corrupted: %v %v", a.Mode(), b.Mode())
	}
	if os.SameFile(a, b) {
		t.Fatal("metadata-changing file shared old inode")
	}
}
func TestUnusualNamesAndSymlinks(t *testing.T) {
	p := fixture(t)
	name := "-中文 with spaces\nand newline.txt"
	write(t, filepath.Join(p.Sources[0].Path, name), "exact contents")
	os.Symlink("/etc", filepath.Join(p.Sources[0].Path, "outside"))
	entries, _, e := Scan(context.Background(), p)
	if e != nil {
		t.Fatal(e)
	}
	if len(entries) != 2 {
		t.Fatal("scanner followed symlink")
	}
	sn, e := Execute(context.Background(), p, p.Destinations[0], entries, nil, DetectRsync(""), func(string) {})
	if e != nil {
		t.Fatal(e)
	}
	if e = Verify(context.Background(), sn.Path, entries); e != nil {
		t.Fatal(e)
	}
}
func TestRepositorySymlinkRejected(t *testing.T) {
	p := fixture(t)
	entries, _, _ := Scan(context.Background(), p)
	repo, root, e := OpenRepository(p.Destinations[0], true)
	_ = repo
	if e != nil {
		t.Fatal(e)
	}
	planRoot := filepath.Join(root, "plans", p.ID)
	os.MkdirAll(planRoot, 0700)
	outside := t.TempDir()
	os.Symlink(outside, filepath.Join(planRoot, "snapshots"))
	if _, e = Execute(context.Background(), p, p.Destinations[0], entries, nil, DetectRsync(""), func(string) {}); e == nil {
		t.Fatal("accepted symlink in repository")
	}
	rows, _ := os.ReadDir(outside)
	if len(rows) > 0 {
		t.Fatal("wrote outside repository")
	}
}

func TestDisabledSourceNotPublished(t *testing.T) {
	p := fixture(t)
	p.Sources[1].Enabled = false
	write(t, filepath.Join(p.Sources[1].Path, "disabled.txt"), "excluded source")
	entries, _, e := Scan(context.Background(), p)
	if e != nil {
		t.Fatal(e)
	}
	sn, e := Execute(context.Background(), p, p.Destinations[0], entries, nil, DetectRsync(""), func(string) {})
	if e != nil {
		t.Fatal(e)
	}
	if len(sn.Sources) != 1 || len(entries) != 0 {
		t.Fatal("disabled source advertised in snapshot")
	}
	if _, e = os.Stat(filepath.Join(sn.Path, p.Sources[1].ID)); !os.IsNotExist(e) {
		t.Fatal("disabled source copied")
	}
}

func TestIncrementalViewRetainsDeletedAndRenamedPaths(t *testing.T) {
	p := fixture(t)
	p.Mode = "incremental"
	ctx := context.Background()
	root := p.Sources[0].Path
	write(t, filepath.Join(root, "deleted.txt"), "keep me")
	write(t, filepath.Join(root, "moved.txt"), "same content")
	old, _, e := Scan(ctx, p)
	if e != nil {
		t.Fatal(e)
	}
	first, e := Execute(ctx, p, p.Destinations[0], old, nil, DetectRsync(""), func(string) {})
	if e != nil {
		t.Fatal(e)
	}
	os.Remove(filepath.Join(root, "deleted.txt"))
	os.Rename(filepath.Join(root, "moved.txt"), filepath.Join(root, "new-name.txt"))
	now, _, e := Scan(ctx, p)
	if e != nil {
		t.Fatal(e)
	}
	view := BuildView(p, now, first)
	if len(view.Retained) != 2 {
		t.Fatalf("not retained: %+v", view)
	}
	second, e := Execute(ctx, p, p.Destinations[0], now, first, DetectRsync(""), func(string) {})
	if e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"deleted.txt", "moved.txt", "new-name.txt"} {
		if _, e = os.Stat(filepath.Join(second.Path, p.Sources[0].ID, name)); e != nil {
			t.Fatal(e)
		}
	}
	if e = Verify(ctx, first.Path, first.Entries); e != nil {
		t.Fatal("old generation changed", e)
	}
	current, e := ReadCurrent(p.Destinations[0], p.ID)
	if e != nil || current.ID != second.ID {
		t.Fatalf("current not switched: %+v %v", current, e)
	}
	if !EquivalentTrees(second.Entries, view.Entries) {
		t.Fatal("incremental manifest incomplete")
	}
}
func TestMirrorProtectsExcludedAndDisabledSources(t *testing.T) {
	p := fixture(t)
	p.Mode = "mirror"
	ctx := context.Background()
	write(t, filepath.Join(p.Sources[0].Path, "deleted.txt"), "delete from current")
	write(t, filepath.Join(p.Sources[0].Path, "protected.tmp"), "keep excluded")
	write(t, filepath.Join(p.Sources[1].Path, "disabled.txt"), "keep disabled")
	old, _, _ := Scan(ctx, p)
	first, e := Execute(ctx, p, p.Destinations[0], old, nil, DetectRsync(""), func(string) {})
	if e != nil {
		t.Fatal(e)
	}
	os.Remove(filepath.Join(p.Sources[0].Path, "deleted.txt"))
	os.Remove(filepath.Join(p.Sources[0].Path, "protected.tmp"))
	p.Sources[0].Rules = []Rule{{Action: "exclude", Pattern: "*.tmp"}}
	p.Sources[1].Enabled = false
	now, _, e := Scan(ctx, p)
	if e != nil {
		t.Fatal(e)
	}
	view := BuildView(p, now, first)
	preview := DescribeView(view, first, p.Destinations[0].ID, p.Mode)
	if preview.DeletePaths != 1 || preview.Retained != 2 {
		t.Fatalf("incorrect protected scope: %+v", preview)
	}
	if _, e = Execute(ctx, p, p.Destinations[0], now, first, DetectRsync(""), func(string) {}); e == nil {
		t.Fatal("executor allowed unapproved mirror deletion")
	}
	p.MirrorDeletesAuthorized = true
	second, e := Execute(ctx, p, p.Destinations[0], now, first, DetectRsync(""), func(string) {})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(second.Path, p.Sources[0].ID, "deleted.txt")); !os.IsNotExist(e) {
		t.Fatal("mirror retained unprotected deleted path")
	}
	if _, e = os.Stat(filepath.Join(second.Path, p.Sources[0].ID, "protected.tmp")); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(second.Path, p.Sources[1].ID, "disabled.txt")); e != nil {
		t.Fatal(e)
	}
	if e = Verify(ctx, first.Path, first.Entries); e != nil {
		t.Fatal(e)
	}
}
func TestIncrementalFileDirectoryReplacementKeepsHistory(t *testing.T) {
	p := fixture(t)
	p.Mode = "incremental"
	ctx := context.Background()
	root := p.Sources[0].Path
	write(t, filepath.Join(root, "directory/child.txt"), "old child")
	write(t, filepath.Join(root, "file"), "old file")
	old, _, _ := Scan(ctx, p)
	first, e := Execute(ctx, p, p.Destinations[0], old, nil, DetectRsync(""), func(string) {})
	if e != nil {
		t.Fatal(e)
	}
	os.RemoveAll(filepath.Join(root, "directory"))
	write(t, filepath.Join(root, "directory"), "now a file")
	os.Remove(filepath.Join(root, "file"))
	write(t, filepath.Join(root, "file/new-child.txt"), "now a directory")
	next, _, e := Scan(ctx, p)
	if e != nil {
		t.Fatal(e)
	}
	second, e := Execute(ctx, p, p.Destinations[0], next, first, DetectRsync(""), func(string) {})
	if e != nil {
		t.Fatal(e)
	}
	if e = Verify(ctx, second.Path, second.Entries); e != nil {
		t.Fatal(e)
	}
	if e = Verify(ctx, first.Path, first.Entries); e != nil {
		t.Fatal("type conflict damaged history", e)
	}
}

func TestRepositoryLockPreventsCompetingWriterAndRestore(t *testing.T) {
	p := fixture(t)
	ctx := context.Background()
	write(t, filepath.Join(p.Sources[0].Path, "locked.txt"), "protected")
	entries, _, _ := Scan(ctx, p)
	first, e := Execute(ctx, p, p.Destinations[0], entries, nil, DetectRsync(""), func(string) {})
	if e != nil {
		t.Fatal(e)
	}
	unlock, e := LockRepository(p.Destinations[0], true, false)
	if e != nil {
		t.Fatal(e)
	}
	defer unlock()
	if release, e := LockRepository(p.Destinations[0], true, false); e == nil {
		release()
		t.Fatal("competing repository writer accepted")
	}
	if _, e = Restore(ctx, *first, RestoreRequest{SourceID: p.Sources[0].ID, Destination: t.TempDir()}, []Plan{p}, DetectRsync("")); e == nil {
		t.Fatal("restore read-lock did not respect competing writer")
	}
}
func TestMirrorPreviewIncludesEmptyDirectoryDeletion(t *testing.T) {
	p := fixture(t)
	p.Mode = "mirror"
	ctx := context.Background()
	os.Mkdir(filepath.Join(p.Sources[0].Path, "empty"), 0755)
	old, _, _ := Scan(ctx, p)
	first, e := Execute(ctx, p, p.Destinations[0], old, nil, DetectRsync(""), func(string) {})
	if e != nil {
		t.Fatal(e)
	}
	os.Remove(filepath.Join(p.Sources[0].Path, "empty"))
	now, _, _ := Scan(ctx, p)
	view := BuildView(p, now, first)
	v := DescribeView(view, first, p.Destinations[0].ID, p.Mode)
	if v.DeletePaths != 1 || len(v.Removals) != 1 || v.Removals[0].Path != "empty" || v.Deleted != 1 {
		t.Fatalf("empty directory absent from deletion preview %+v", v)
	}
}

func TestSameIDCurrentManifestChangeInvalidatesPreparedMirror(t *testing.T) {
	p := fixture(t)
	p.Mode = "mirror"
	ctx := context.Background()
	write(t, filepath.Join(p.Sources[0].Path, "original.txt"), "original")
	old, _, _ := Scan(ctx, p)
	first, e := Execute(ctx, p, p.Destinations[0], old, nil, DetectRsync(""), func(string) {})
	if e != nil {
		t.Fatal(e)
	}
	d := p.Destinations[0]
	p.ExpectedBaselines = map[string]string{d.ID: first.ID}
	p.ExpectedCurrentIDs = map[string]string{d.ID: first.ID}
	p.ExpectedBaselineDigests = map[string]string{d.ID: SnapshotDigest(*first)}
	p.MirrorDeletesAuthorized = true
	tampered := *first
	tampered.PlanName = "externally modified metadata"
	raw, _ := json.Marshal(tampered)
	if e = os.WriteFile(filepath.Join(first.Path, "manifest.json"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	os.Remove(filepath.Join(p.Sources[0].Path, "original.txt"))
	next, _, _ := Scan(ctx, p)
	if _, e = Execute(ctx, p, d, next, first, DetectRsync(""), func(string) {}); e == nil {
		t.Fatal("same-ID manifest edit bypassed prepared baseline")
	}
	current, e := ReadCurrent(d, p.ID)
	if e != nil || current.ID != first.ID {
		t.Fatal("changed baseline was overwritten")
	}
	if _, e = os.Stat(filepath.Join(first.Path, p.Sources[0].ID, "original.txt")); e != nil {
		t.Fatal("old data removed")
	}
}

func TestFilesystemNameCollisionPreflight(t *testing.T) {
	v := View{Sources: []Source{{ID: "src-one"}}, Entries: []Entry{{SourceID: "src-one", Path: "Readme.txt", Kind: "file"}, {SourceID: "src-one", Path: "README.txt", Kind: "file"}}}
	if e := ValidateViewNames(v, FilesystemNames{CaseSensitive: false}); e == nil {
		t.Fatal("case collision accepted")
	}
	if e := ValidateViewNames(v, FilesystemNames{CaseSensitive: true}); e != nil {
		t.Fatal(e)
	}
	v.Entries = []Entry{{SourceID: "src-one", Path: "\u00e9.txt", Kind: "file"}, {SourceID: "src-one", Path: "e\u0301.txt", Kind: "file"}}
	if e := ValidateViewNames(v, FilesystemNames{CaseSensitive: true, NormalizesUnicode: true}); e == nil {
		t.Fatal("normalization collision accepted")
	}
	caps, e := ProbeFilesystemNames(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	expectedFailure := caps.NormalizesUnicode
	if (ValidateViewNames(v, caps) != nil) != expectedFailure {
		t.Fatal("real filesystem capability not respected")
	}
}
func TestPhysicalOverlapRejectsCaseAliases(t *testing.T) {
	base := t.TempDir()
	parent := filepath.Join(base, "MixedCase")
	child := filepath.Join(parent, "Nested")
	os.MkdirAll(child, 0755)
	alias := filepath.Join(base, "mixedcase")
	if st, e := os.Stat(alias); e != nil || !st.IsDir() {
		t.Skip("requires case-insensitive filesystem")
	}
	expected, _ := filepath.EvalSymlinks(parent)
	canonical, e := Canonical(alias)
	if e != nil || canonical != expected {
		t.Fatalf("canonical case alias not resolved: %s %v", canonical, e)
	}
	if !PhysicalContains(alias, child) || !PathsOverlap(alias, child) {
		t.Fatal("physical parent-child alias escaped overlap protection")
	}
	p := fixture(t)
	p.Sources[0].ID = "Source-A"
	p.Sources[1].ID = "source-a"
	if e := Validate(&p); e == nil {
		t.Fatal("case-aliased source namespaces accepted")
	}
}

func TestRestoreRejectsUntrackedAndTamperedSymlink(t *testing.T) {
	p := fixture(t)
	root := p.Sources[0].Path
	write(t, filepath.Join(root, "file.txt"), "known file")
	os.Symlink("file.txt", filepath.Join(root, "link"))
	entries, _, _ := Scan(context.Background(), p)
	sn, e := Execute(context.Background(), p, p.Destinations[0], entries, nil, DetectRsync(""), func(string) {})
	if e != nil {
		t.Fatal(e)
	}
	backupRoot := filepath.Join(sn.Path, p.Sources[0].ID)
	os.WriteFile(filepath.Join(backupRoot, "untracked.txt"), []byte("untracked"), 0644)
	out := t.TempDir()
	if _, e = Restore(context.Background(), *sn, RestoreRequest{SourceID: p.Sources[0].ID, Path: "untracked.txt", Destination: out}, []Plan{p}, DetectRsync("")); e == nil {
		t.Fatal("untracked file restored as legitimate snapshot data")
	}
	os.Remove(filepath.Join(backupRoot, "link"))
	os.Symlink("unexpected-target", filepath.Join(backupRoot, "link"))
	if _, e = Restore(context.Background(), *sn, RestoreRequest{SourceID: p.Sources[0].ID, Path: "link", Destination: out}, []Plan{p}, DetectRsync("")); e == nil {
		t.Fatal("tampered symlink restored without verification")
	}
	rows, _ := os.ReadDir(out)
	if len(rows) != 0 {
		t.Fatal("invalid restore created output before verification")
	}
}
func TestDirectoryRestoreTransfersOnlyManifestEntries(t *testing.T) {
	p := fixture(t)
	write(t, filepath.Join(p.Sources[0].Path, "nested/known.txt"), "listed content")
	entries, _, _ := Scan(context.Background(), p)
	sn, e := Execute(context.Background(), p, p.Destinations[0], entries, nil, DetectRsync(""), func(string) {})
	if e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(sn.Path, p.Sources[0].ID, "nested/untracked.txt"), []byte("outside manifest"), 0644)
	dest, e := Restore(context.Background(), *sn, RestoreRequest{SourceID: p.Sources[0].ID, Path: "nested", Destination: t.TempDir()}, []Plan{p}, DetectRsync(""))
	if e != nil {
		t.Fatal(e)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "known.txt")); string(b) != "listed content" {
		t.Fatal("listed file missing")
	}
	if _, e = os.Stat(filepath.Join(dest, "untracked.txt")); !os.IsNotExist(e) {
		t.Fatal("untracked historical content leaked into restore")
	}
}

func TestFailedRestoreReportsPartialOutputWithoutOverwritingSource(t *testing.T) {
	p := fixture(t)
	source := filepath.Join(p.Sources[0].Path, "original.txt")
	write(t, source, "original content")
	entries, _, _ := Scan(context.Background(), p)
	sn, e := Execute(context.Background(), p, p.Destinations[0], entries, nil, DetectRsync(""), func(string) {})
	if e != nil {
		t.Fatal(e)
	}
	dest, e := Restore(context.Background(), *sn, RestoreRequest{SourceID: p.Sources[0].ID, Path: "original.txt", Destination: t.TempDir()}, []Plan{p}, RsyncInfo{Path: "/usr/bin/false", Available: true})
	if e == nil || dest == "" {
		t.Fatal("failed restore did not report its output directory")
	}
	if st, err := os.Stat(dest); err != nil || !st.IsDir() {
		t.Fatal("partial output not available for diagnostics")
	}
	if data, _ := os.ReadFile(source); string(data) != "original content" {
		t.Fatal("failed restore overwrote source")
	}
}

func TestNestedExcludedFilesNeverEnterSnapshot(t *testing.T) {
	p := fixture(t)
	root := p.Sources[0].Path
	write(t, filepath.Join(root, "included/keep.txt"), "keep")
	write(t, filepath.Join(root, "included/secret.tmp"), "must not copy")
	os.Mkdir(filepath.Join(root, "empty"), 0750)
	p.Sources[0].Rules = []Rule{{Action: "exclude", Pattern: "*.tmp"}}
	entries, _, e := Scan(context.Background(), p)
	if e != nil {
		t.Fatal(e)
	}
	sn, e := Execute(context.Background(), p, p.Destinations[0], entries, nil, DetectRsync(""), func(string) {})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(sn.Path, p.Sources[0].ID, "included/secret.tmp")); !os.IsNotExist(e) {
		t.Fatal("excluded child copied via implicit directory transfer")
	}
	if st, e := os.Stat(filepath.Join(sn.Path, p.Sources[0].ID, "empty")); e != nil || !st.IsDir() || st.Mode().Perm() != 0750 {
		t.Fatal("empty directory metadata lost")
	}
	if e = VerifyTreeLayout(context.Background(), sn.Path, sn.Entries, sn.Sources, true); e != nil {
		t.Fatal(e)
	}
}

func TestRelativeToVolumeNeverEscapesMount(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "folder")
	os.Mkdir(target, 0755)
	relative, err := RelativeToVolume(root, target)
	if err != nil || relative != "folder" {
		t.Fatalf("%q %v", relative, err)
	}
	if _, err = RelativeToVolume(root, t.TempDir()); err == nil {
		t.Fatal("accepted directory outside mount")
	}
	if runtime.GOOS == "darwin" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		mount, err := mountPoint(home)
		if err != nil {
			t.Fatal(err)
		}
		relative, err = RelativeToVolume(mount, home)
		if err != nil || !SafeRelative(relative) {
			t.Fatalf("firmlink produced escaping relative path: %q %v", relative, err)
		}
		a, _ := os.Stat(home)
		b, _ := os.Stat(filepath.Join(mount, relative))
		if b == nil || !os.SameFile(a, b) {
			t.Fatal("relative path does not reference selected directory")
		}
	}
}
