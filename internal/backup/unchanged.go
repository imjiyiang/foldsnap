package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func unchangedView(p Plan, d Destination, view View, previous *Snapshot, modern bool) (bool, error) {
	if previous == nil || previous.ExtendedMetadata != modern || previous.Mode != p.Mode || previous.PlanConfig == nil {
		return false, nil
	}
	config := p
	if p.RecoveryPlan != nil {
		config = *p.RecoveryPlan
	}
	a, err := json.Marshal(config)
	if err != nil {
		return false, err
	}
	b, err := json.Marshal(previous.PlanConfig)
	if err != nil {
		return false, err
	}
	if !bytes.Equal(a, b) {
		return false, nil
	}
	a, _ = json.Marshal(view.Sources)
	b, _ = json.Marshal(previous.Sources)
	if !bytes.Equal(a, b) || len(view.Entries) != len(previous.Entries) {
		return false, nil
	}
	old := make(map[string]Entry, len(previous.Entries))
	for _, en := range previous.Entries {
		if _, ok := old[entryKey(en)]; ok {
			return false, fmt.Errorf("基线包含重复路径")
		}
		old[entryKey(en)] = en
	}
	for _, en := range view.Entries {
		prior, ok := old[entryKey(en)]
		if !ok || !sameMetadata(en, prior) || (modern && (en.BasicMetadata != prior.BasicMetadata || (!en.BasicMetadata && en.AttributeHash != prior.AttributeHash))) {
			return false, nil
		}
		delete(old, entryKey(en))
	}
	if p.Mode != "snapshot" {
		current, err := ReadCurrent(d, p.ID)
		if err != nil {
			return false, err
		}
		// A missing current pointer needs an actual publication, even if an old
		// history point happens to contain the same files.
		if current == nil || current.ID != previous.ID {
			return false, nil
		}
	}
	return true, nil
}

func sameSourceObservation(a, b Entry) bool {
	return entryKey(a) == entryKey(b) && a.Kind == b.Kind && a.Size == b.Size && a.ModTime == b.ModTime && a.Mode == b.Mode && a.Identity == b.Identity && a.FileID == b.FileID && a.ChangeTime == b.ChangeTime && a.BirthTime == b.BirthTime && a.AttributeHash == b.AttributeHash && a.Hash == b.Hash && a.Link == b.Link && (a.VolumeID == "" || a.VolumeID == b.VolumeID)
}

// This fully reads source content, independently of any SQLite fingerprint.
// Before/after version checks bind attributes and content to the scanned path.
func verifySourceEntry(ctx context.Context, src Source, en Entry) error {
	if !SafeRelative(en.Path) || en.SourceID != src.ID {
		return fmt.Errorf("源校验路径越界")
	}
	if err := Checkpoint(ctx); err != nil {
		return err
	}
	path := filepath.Join(src.Path, filepath.FromSlash(en.Path))
	if err := SafeTree(src.Path, filepath.Dir(path)); err != nil {
		return err
	}
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	actual := Entry{SourceID: en.SourceID, Path: en.Path, Kind: entryKind(st), Size: st.Size(), ModTime: st.ModTime().UnixNano(), Mode: uint32(st.Mode().Perm())}
	populateIdentity(&actual, st, en.VolumeID)
	if actual.Kind == "directory" {
		actual.Size = 0
	}
	actual.AttributeHash, err = AttributeHash(path)
	if err != nil {
		return err
	}
	switch actual.Kind {
	case "file":
		actual.Hash, err = hashStableFile(ctx, path, st)
	case "symlink":
		actual.Link, err = os.Readlink(path)
	case "directory":
	default:
		return fmt.Errorf("源文件类型无效: %s", en.Path)
	}
	if err != nil {
		return err
	}
	latest, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !samePhysicalVersion(st, latest) || !sameSourceObservation(en, actual) {
		return fmt.Errorf("源内容、属性或物理版本与扫描结果不一致: %s", en.Path)
	}
	return nil
}

// Only hashes just proven by complete reads are reusable for the closing full
// selection scan. This ephemeral index never writes SQLite or trusts a journal.
type verifiedSourceHashes map[string]map[string]string

func (verifiedSourceHashes) BeginSource(context.Context, string) (int64, error) { return 0, nil }
func (v verifiedSourceHashes) SourceHashes(_ context.Context, id string) (map[string]string, error) {
	out := map[string]string{}
	for key, hash := range v[id] {
		out[key] = hash
	}
	return out, nil
}
func (verifiedSourceHashes) LookupHash(context.Context, string) (string, error) { return "", nil }
func (verifiedSourceHashes) CommitSource(context.Context, SourceScope, []IndexedPath) error {
	return nil
}

// Path/repository identity and the complete manifest are checked from one read.
func validateBaselineManifest(previous Snapshot) (string, error) {
	root, disk, err := readSnapshotManifest(previous)
	if err != nil {
		return "", err
	}
	// Pinning is database state. A validated mount rebind may change spelling.
	disk.Pinned = previous.Pinned
	disk.Path = previous.Path
	disk.DestinationPath = previous.DestinationPath
	disk.MountPath = previous.MountPath
	if SnapshotDigest(disk) != SnapshotDigest(previous) {
		return "", fmt.Errorf("已有恢复点清单与已确认基线不一致")
	}
	return root, nil
}

func verifyUnchanged(ctx context.Context, p Plan, d Destination, entries []Entry, previous Snapshot, log func(string)) error {
	root, err := validateBaselineManifest(previous)
	if err != nil {
		return err
	}
	versions, err := snapshotVersions(ctx, previous)
	if err != nil {
		return err
	}
	baselineReads := newVerificationReads(ctx)
	Report(ctx, WorkEvent{Phase: "verify_baseline", Destination: d.Name})
	if err = auditSnapshotContents(baselineReads.ctx, previous, root); err != nil {
		return fmt.Errorf("目标基线完整性校验失败: %w", err)
	}
	log(baselineReads.summary("已有恢复点内容校验"))
	Report(ctx, WorkEvent{Phase: "verify_source", Destination: d.Name})
	sourceReads := newVerificationReads(ctx)
	sources := map[string]Source{}
	for _, src := range p.Sources {
		if src.Enabled {
			if _, err := BindSource(src); err != nil {
				return err
			}
			sources[src.ID] = src
		}
	}
	hashes := verifiedSourceHashes{}
	for _, en := range entries {
		src, ok := sources[en.SourceID]
		if !ok {
			return fmt.Errorf("源校验范围不一致")
		}
		if err := verifySourceEntry(sourceReads.ctx, src, en); err != nil {
			return err
		}
		if en.Kind == "file" {
			if hashes[en.SourceID] == nil {
				hashes[en.SourceID] = map[string]string{}
			}
			hashes[en.SourceID][ContentKey(en)] = en.Hash
		}
	}
	log(sourceReads.summary("源文件内容校验"))
	Report(ctx, WorkEvent{Phase: "verify_closing", Destination: d.Name})
	closingStart := time.Now()
	// Catch selected additions/deletions and changes to an earlier file while
	// another file/source was being checked. Never reuse partial FSEvents views.
	p.RuntimeDirtyRoots = nil
	observed, err := ScanIndexed(ctx, p, hashes, false)
	if err != nil {
		return err
	}
	expected := make(map[string]Entry, len(entries))
	for _, en := range entries {
		expected[entryKey(en)] = en
	}
	if len(expected) != len(entries) || len(observed.Entries) != len(entries) {
		return fmt.Errorf("完整校验期间源选择集合已变化，请重新预览")
	}
	for _, en := range observed.Entries {
		prior, ok := expected[entryKey(en)]
		if !ok || !sameSourceObservation(prior, en) {
			return fmt.Errorf("完整校验期间源文件已变化: %s", en.Path)
		}
	}
	log(fmt.Sprintf("源目录结束复核完成：%d 个路径，额外内容读取 %d B，用时 %.3f 秒", observed.Stats.PathsVisited, observed.Stats.BytesHashed, time.Since(closingStart).Seconds()))
	for path, before := range versions {
		if err = Checkpoint(ctx); err != nil {
			return err
		}
		latest, e := os.Lstat(path)
		if e != nil || !samePhysicalVersion(before, latest) {
			return fmt.Errorf("完整校验期间恢复点发生变化: %s", path)
		}
	}
	if err = VerifyTreeLayout(ctx, previous.Path, previous.Entries, previous.Sources, true); err != nil {
		return err
	}
	if _, err = CheckDestination(d); err != nil {
		return err
	}
	repo, _, err := OpenRepository(d, false)
	if err != nil {
		return err
	}
	if repo.ID != previous.RepositoryID {
		return fmt.Errorf("完整校验期间仓库身份已变化")
	}
	if _, err = validateBaselineManifest(previous); err != nil {
		return err
	}
	if err = checkCurrentBaseline(p, d); err != nil {
		return err
	}
	return Checkpoint(ctx)
}

// Bind the one complete baseline audit to the versions that remain at completion.
// Reads never need to change the reference count of a historical inode.
func snapshotVersions(ctx context.Context, s Snapshot) (map[string]os.FileInfo, error) {
	paths := []string{s.Path, filepath.Join(s.Path, "manifest.json")}
	for _, src := range s.Sources {
		if !ValidID(src.ID) {
			return nil, fmt.Errorf("基线源 ID 无效")
		}
		paths = append(paths, filepath.Join(s.Path, src.ID))
	}
	for _, en := range s.Entries {
		if !ValidID(en.SourceID) || !SafeRelative(en.Path) {
			return nil, fmt.Errorf("基线路径无效")
		}
		paths = append(paths, filepath.Join(s.Path, en.SourceID, filepath.FromSlash(en.Path)))
	}
	versions := make(map[string]os.FileInfo, len(paths))
	for _, path := range paths {
		if err := Checkpoint(ctx); err != nil {
			return nil, err
		}
		if path != s.Path {
			if err := SafeTree(s.Path, filepath.Dir(path)); err != nil {
				return nil, err
			}
		}
		st, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		versions[path] = st
	}
	return versions, nil
}

// Counts logical file-content reads, separate from device counters and xattrs.
// The callback is serial, like the underlying verifier; it forwards all work.
type verificationReads struct {
	ctx     context.Context
	started time.Time
	files   int
	bytes   int64
}

func newVerificationReads(ctx context.Context) *verificationReads {
	r := &verificationReads{started: time.Now()}
	r.ctx = Observe(ctx, func(e WorkEvent) {
		if e.Path != "" && !e.CacheHit {
			r.files++
		}
		r.bytes += e.Bytes
		Report(ctx, e)
	})
	return r
}
func (r *verificationReads) summary(label string) string {
	return fmt.Sprintf("%s完成：%d 个文件，内容读取 %d B，用时 %.3f 秒", label, r.files, r.bytes, time.Since(r.started).Seconds())
}
