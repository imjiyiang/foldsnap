package store

import (
	"context"
	"localbackup/internal/backup"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func indexedFixture(t *testing.T) (*Store, backup.Plan) {
	t.Helper()
	root := t.TempDir()
	source, target := filepath.Join(root, "source"), filepath.Join(root, "target")
	os.Mkdir(source, 0755)
	os.Mkdir(target, 0755)
	st, err := Open(filepath.Join(root, "state", "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.DB.Close() })
	p := backup.Plan{ID: "plan-index", Name: "Index", Mode: "snapshot", Sources: []backup.Source{{ID: "src-index", Name: "Files", Path: source, TargetPath: "Files", Enabled: true}}, Destinations: []backup.Destination{{ID: "dst-index", Name: "Target", Path: target, Enabled: true}}}
	if err = backup.Validate(&p); err != nil {
		t.Fatal(err)
	}
	return st, p
}
func indexedScan(t *testing.T, st *Store, p backup.Plan, force bool) backup.ScanResult {
	t.Helper()
	r, err := backup.ScanIndexed(context.Background(), p, st, force)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestPersistentFingerprintsAndSameMtimeChanges(t *testing.T) {
	st, p := indexedFixture(t)
	path := filepath.Join(p.Sources[0].Path, "a.txt")
	os.WriteFile(path, []byte("AAA"), 0644)
	first := indexedScan(t, st, p, false)
	if first.Stats.FilesHashed != 1 || first.Entries[0].ChangeTime == 0 || first.Entries[0].FileID == "" {
		t.Fatal("missing file identity", first)
	}
	second := indexedScan(t, st, p, false)
	status, err := st.IndexStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Sources[0].Cacheable {
		if second.Stats.CacheHits != 1 || second.Stats.FilesHashed != 0 || second.Stats.BytesReused != 3 {
			t.Fatal("unchanged file was re-read", second.Stats)
		}
	} else if second.Stats.FilesHashed != 1 {
		t.Fatal("unknown filesystem must hash")
	}
	original, _ := os.Stat(path)
	os.WriteFile(path, []byte("BBB"), 0644)
	os.Chtimes(path, original.ModTime(), original.ModTime())
	changed := indexedScan(t, st, p, false)
	if changed.Entries[0].Hash == first.Entries[0].Hash || changed.Stats.FilesHashed != 1 {
		t.Fatal("same-size restored-mtime edit reused a stale hash")
	}
	full := indexedScan(t, st, p, true)
	if full.Stats.CacheHits != 0 || full.Stats.FilesHashed != 1 || !full.Stats.Forced {
		t.Fatal("forced audit used cache")
	}
	// Reopen the real SQLite file to prove this is a persistent cache/index.
	pathDB := st.Path
	st.DB.Close()
	reopened, err := Open(pathDB)
	if err != nil {
		t.Fatal(err)
	}
	st.DB = reopened.DB
	again := indexedScan(t, st, p, false)
	if status.Sources[0].Cacheable && again.Stats.CacheHits != 1 {
		t.Fatal("cache did not survive reopening")
	}
	if again.Entries[0].Hash != changed.Entries[0].Hash {
		t.Fatal("reopened cache changed content identity")
	}
}
func TestIndexGenerationPrunesPathsAndKeepsExclusionState(t *testing.T) {
	st, p := indexedFixture(t)
	source := p.Sources[0].Path
	os.Mkdir(filepath.Join(source, "cache"), 0755)
	os.WriteFile(filepath.Join(source, "cache/old.bin"), []byte("cache"), 0644)
	os.WriteFile(filepath.Join(source, "keep.txt"), []byte("keep"), 0644)
	indexedScan(t, st, p, false)
	p.Rules = []backup.Rule{{Action: "exclude", Pattern: "cache/**"}}
	os.Remove(filepath.Join(source, "keep.txt"))
	result := indexedScan(t, st, p, false)
	status, err := st.IndexStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Paths != 1 || status.Excluded != 1 || status.Selected != 0 || len(result.Ignored) != 1 {
		t.Fatalf("stale paths survived: %+v", status)
	}
	var reason string
	st.DB.QueryRow("SELECT reason FROM source_paths WHERE relative_path='cache'").Scan(&reason)
	if !strings.Contains(reason, "cache") {
		t.Fatal("excluded directory lost reason")
	}
}
func TestCancelledScanDoesNotPublishIndexGeneration(t *testing.T) {
	st, p := indexedFixture(t)
	source := p.Sources[0].Path
	os.WriteFile(filepath.Join(source, "first.txt"), []byte("one"), 0644)
	indexedScan(t, st, p, true)
	before, _ := st.IndexStatus(context.Background())
	os.WriteFile(filepath.Join(source, "second.txt"), []byte("two"), 0644)
	ctx, cancel := context.WithCancel(context.Background())
	ctx = backup.Observe(ctx, func(e backup.WorkEvent) {
		if e.Path != "" {
			cancel()
		}
	})
	if _, err := backup.ScanIndexed(ctx, p, st, true); err == nil {
		t.Fatal("cancelled scan succeeded")
	}
	after, _ := st.IndexStatus(context.Background())
	if after.Paths != before.Paths || after.Sources[0].Generation != before.Sources[0].Generation {
		t.Fatal("partial scan replaced completed index")
	}
}
func TestCacheIsNotPublicationAuthorityAndFullScanRepairsIt(t *testing.T) {
	st, p := indexedFixture(t)
	os.WriteFile(filepath.Join(p.Sources[0].Path, "a.txt"), []byte("verified content"), 0644)
	first := indexedScan(t, st, p, false)
	status, _ := st.IndexStatus(context.Background())
	if !status.Sources[0].Cacheable {
		t.Skip("requires a trustworthy persistent volume/filesystem")
	}
	st.DB.Exec("UPDATE filesystem_objects SET content_hash=?", strings.Repeat("0", 64))
	healed := indexedScan(t, st, p, false)
	if healed.Stats.CacheHits != 0 || healed.Entries[0].Hash != first.Entries[0].Hash {
		t.Fatal("invalid integrity tag was trusted")
	}
	// Inject an artificially signed bad value to verify the final target proof
	// independently of the cache integrity layer.
	key := backup.ContentKey(first.Entries[0])
	bad := strings.Repeat("0", 64)
	st.DB.Exec("UPDATE filesystem_objects SET content_hash=?,auth_tag=? WHERE object_key=?", bad, st.cacheTag(key, bad), key)
	cached := indexedScan(t, st, p, false)
	if cached.Stats.CacheHits != 1 {
		t.Fatal("cache tamper fixture not exercised")
	}
	rs := backup.DetectRsync("")
	if !rs.Available {
		t.Skip("requires rsync")
	}
	if _, err := backup.Execute(context.Background(), p, p.Destinations[0], cached.Entries, nil, rs, func(string) {}); err == nil {
		t.Fatal("corrupted cached hash bypassed verification")
	}
	files, _ := filepath.Glob(filepath.Join(p.Destinations[0].Path, "LocalBackup", "plans", p.ID, "snapshots", "*"))
	if len(files) > 0 {
		t.Fatal("unchecked snapshot published")
	}
	repaired := indexedScan(t, st, p, true)
	if repaired.Entries[0].Hash != first.Entries[0].Hash {
		t.Fatal("full scan did not repair fingerprint")
	}
	snapshot, err := backup.Execute(context.Background(), p, p.Destinations[0], repaired.Entries, nil, rs, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if err = backup.AuditSnapshot(context.Background(), *snapshot); err != nil {
		t.Fatal(err)
	}
}
func TestObjectKeyIncludesVolumeBirthAndChangeTime(t *testing.T) {
	a := backup.Entry{Kind: "file", VolumeID: "volume-a", FileID: "123", Size: 3, ModTime: 4, ChangeTime: 5, BirthTime: 6}
	key := backup.ContentKey(a)
	if key == "" {
		t.Fatal("empty physical key")
	}
	b := a
	b.VolumeID = "volume-b"
	if backup.ContentKey(b) == key {
		t.Fatal("cross-volume collision")
	}
	b = a
	b.BirthTime++
	if backup.ContentKey(b) == key {
		t.Fatal("reused inode collided")
	}
	b = a
	b.ChangeTime++
	if backup.ContentKey(b) == key {
		t.Fatal("changed version collided")
	}
	b = a
	b.FileID = ""
	if backup.ContentKey(b) != "" {
		t.Fatal("unknown file identity cached")
	}
}
func TestOlderScanCannotOverwriteNewerGeneration(t *testing.T) {
	st, p := indexedFixture(t)
	source := p.Sources[0].Path
	os.WriteFile(filepath.Join(source, "a.txt"), []byte("a"), 0644)
	first := indexedScan(t, st, p, true)
	newer := backup.SourceScope{Generation: 10, SourceID: p.Sources[0].ID, Root: source, VolumeID: first.Entries[0].VolumeID, ScannedAt: time.Now().UTC().Add(time.Second), Filesystem: "apfs", Cacheable: true}
	record := backup.IndexedPath{Entry: first.Entries[0], State: "selected", Reason: "new scan", Hashed: true, Cacheable: true}
	if err := st.CommitSource(context.Background(), newer, []backup.IndexedPath{record}); err != nil {
		t.Fatal(err)
	}
	older := newer
	older.Generation = 9
	older.ScannedAt = older.ScannedAt.Add(-time.Hour)
	if err := st.CommitSource(context.Background(), older, nil); err != nil {
		t.Fatal(err)
	}
	status, err := st.IndexStatus(context.Background())
	if err != nil || status.Paths != 1 {
		t.Fatal("older scan removed current index", err)
	}
}

func TestScanOrderingDoesNotDependOnWallClock(t *testing.T) {
	st, p := indexedFixture(t)
	os.WriteFile(filepath.Join(p.Sources[0].Path, "a.txt"), []byte("a"), 0644)
	first := indexedScan(t, st, p, true)
	generation, err := st.BeginSource(context.Background(), p.Sources[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	scope := backup.SourceScope{Generation: generation, SourceID: p.Sources[0].ID, Root: p.Sources[0].Path, ScannedAt: time.Now().Add(-24 * time.Hour)}
	if err = st.CommitSource(context.Background(), scope, []backup.IndexedPath{{Entry: first.Entries[0], State: "selected", Reason: "new logical scan after clock rollback"}}); err != nil {
		t.Fatal(err)
	}
	status, err := st.IndexStatus(context.Background())
	if err != nil || status.Sources[0].Generation != generation {
		t.Fatal("wall clock overrode logical generation", err)
	}
}

func TestMovedReuseChecksActualSourceEvenWithForgedCachedValue(t *testing.T) {
	st, p := indexedFixture(t)
	source := p.Sources[0].Path
	os.WriteFile(filepath.Join(source, "old.txt"), []byte("AAA"), 0644)
	initial := indexedScan(t, st, p, true)
	rs := backup.DetectRsync("")
	if !rs.Available {
		t.Skip("requires rsync")
	}
	old, err := backup.Execute(context.Background(), p, p.Destinations[0], initial.Entries, nil, rs, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	previousInfo, _ := os.Stat(filepath.Join(source, "old.txt"))
	os.Rename(filepath.Join(source, "old.txt"), filepath.Join(source, "new.txt"))
	os.WriteFile(filepath.Join(source, "new.txt"), []byte("BBB"), 0644)
	os.Chtimes(filepath.Join(source, "new.txt"), previousInfo.ModTime(), previousInfo.ModTime())
	changed := indexedScan(t, st, p, true)
	status, _ := st.IndexStatus(context.Background())
	if !status.Sources[0].Cacheable {
		t.Skip("requires trustworthy filesystem")
	}
	key := backup.ContentKey(changed.Entries[0])
	oldHash := initial.Entries[0].Hash
	st.DB.Exec("UPDATE filesystem_objects SET content_hash=?,auth_tag=? WHERE object_key=?", oldHash, st.cacheTag(key, oldHash), key)
	forged := indexedScan(t, st, p, false)
	if forged.Stats.CacheHits != 1 {
		t.Fatal("forged cache fixture not exercised")
	}
	preview := backup.Compare(forged.Entries, old.Entries, p.Destinations[0].ID)
	if preview.Moved != 1 {
		t.Fatal("fixture did not enter moved-file reuse")
	}
	if _, err = backup.Execute(context.Background(), p, p.Destinations[0], forged.Entries, old, rs, func(string) {}); err == nil {
		t.Fatal("moved-file reuse trusted forged cache without source read")
	}
	if err = backup.AuditSnapshot(context.Background(), *old); err != nil {
		t.Fatal("history changed", err)
	}
}
func TestSignedStaleHashCannotPublishMatchingOldBaseline(t *testing.T) {
	st, p := indexedFixture(t)
	path := filepath.Join(p.Sources[0].Path, "same.txt")
	os.WriteFile(path, []byte("AAA"), 0644)
	original := indexedScan(t, st, p, true)
	rs := backup.DetectRsync("")
	if !rs.Available {
		t.Skip("requires rsync")
	}
	first, err := backup.Execute(context.Background(), p, p.Destinations[0], original.Entries, nil, rs, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	os.WriteFile(path, []byte("BBB"), 0644)
	os.Chtimes(path, info.ModTime(), info.ModTime())
	current := indexedScan(t, st, p, true)
	status, _ := st.IndexStatus(context.Background())
	if !status.Sources[0].Cacheable {
		t.Skip("requires trustworthy filesystem")
	}
	key := backup.ContentKey(current.Entries[0])
	st.DB.Exec("UPDATE filesystem_objects SET content_hash=?,auth_tag=? WHERE object_key=?", original.Entries[0].Hash, st.cacheTag(key, original.Entries[0].Hash), key)
	forged := indexedScan(t, st, p, false)
	if forged.Stats.CacheHits != 1 {
		t.Fatal("fixture not exercised")
	}
	if _, err = backup.Execute(context.Background(), p, p.Destinations[0], forged.Entries, first, rs, func(string) {}); err == nil {
		t.Fatal("stale same-path cache hash published old data as current")
	}
	if err = backup.AuditSnapshot(context.Background(), *first); err != nil {
		t.Fatal(err)
	}
}
