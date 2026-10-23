package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type RsyncInfo struct {
	Path      string `json:"path"`
	Version   string `json:"version"`
	Available bool   `json:"available"`
	Modern    bool   `json:"modern"`
}

func DetectRsync(path string) RsyncInfo {
	if path == "" {
		for _, p := range []string{"/opt/homebrew/bin/rsync", "/usr/local/bin/rsync", "/usr/bin/rsync"} {
			if _, e := os.Stat(p); e == nil {
				path = p
				break
			}
		}
	}
	r := RsyncInfo{Path: path}
	if !filepath.IsAbs(path) {
		return r
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b, e := exec.CommandContext(ctx, path, "--version").CombinedOutput()
	if e != nil {
		return r
	}
	if !strings.Contains(strings.ToLower(string(b)), "rsync") {
		return r
	}
	r.Available = true
	r.Version = strings.Split(string(b), "\n")[0]
	r.Modern = strings.Contains(r.Version, "version 3.")
	return r
}
func Arguments(list, src, dst, previous string, modern bool) []string {
	a := []string{"-dltp", "--checksum", "--from0", "--files-from=" + list}
	if modern {
		a = append(a, "-X")
	}
	if previous != "" {
		a = append(a, "--link-dest="+previous)
	}
	a = append(a, "--", src+string(os.PathSeparator), dst+string(os.PathSeparator))
	return a
}
func SafeTree(root, path string) error {
	if !Contains(root, path) {
		return errors.New("路径越过仓库范围")
	}
	rel, _ := filepath.Rel(root, path)
	cur := root
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		if part == "." {
			continue
		}
		cur = filepath.Join(cur, part)
		st, e := os.Lstat(cur)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return e
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return errors.New("仓库内部路径不能包含符号链接")
		}
	}
	return nil
}
func Verify(ctx context.Context, root string, entries []Entry) error {
	for _, e := range entries {
		if err := Checkpoint(ctx); err != nil {
			return err
		}
		if !ValidID(e.SourceID) || !SafeRelative(e.Path) {
			return errors.New("清单路径无效")
		}
		p := filepath.Join(root, e.SourceID, filepath.FromSlash(e.Path))
		if err := SafeTree(root, filepath.Dir(p)); err != nil {
			return err
		}
		st, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if e.Kind != "symlink" && (uint32(st.Mode().Perm()) != e.Mode || (e.Kind == "file" && st.ModTime().Unix() != e.ModTime/1e9)) {
			return fmt.Errorf("权限或修改时间校验失败: %s", e.Path)
		}
		switch e.Kind {
		case "file":
			if !st.Mode().IsRegular() {
				return fmt.Errorf("文件类型发生变化: %s", e.Path)
			}
			hash, er := HashFile(ctx, p)
			if er != nil {
				return er
			}
			if hash != e.Hash || st.Size() != e.Size {
				return fmt.Errorf("内容校验失败: %s", e.Path)
			}
		case "directory":
			if !st.IsDir() {
				return errors.New("目录类型不匹配")
			}
		case "symlink":
			link, er := os.Readlink(p)
			if er != nil || link != e.Link {
				return fmt.Errorf("符号链接校验失败: %s", e.Path)
			}
		default:
			return errors.New("不支持的文件类型")
		}
	}
	return nil
}

// Execute never writes file contents/attributes in previous. Rsync only receives
// paths not already recovered/linked into private staging. Every staged file,
// including linked candidates, receives full verification before publication.
// A nil snapshot with no error means a fully verified unchanged destination.
func Execute(ctx context.Context, p Plan, d Destination, entries []Entry, previous *Snapshot, rsync RsyncInfo, log func(string)) (*Snapshot, error) {
	var sourceErr error
	p, sourceErr = ResolveSources(p)
	if sourceErr != nil {
		return nil, sourceErr
	}
	Report(ctx, WorkEvent{Phase: "prepare", Destination: d.Name})
	selectedEntries := entries
	if !rsync.Available {
		return nil, errors.New("rsync 不可用，请检查设置")
	}
	if err := Checkpoint(ctx); err != nil {
		return nil, err
	}
	// A first backup cannot be unchanged. Reject an invalid full transfer
	// before even initializing its repository metadata.
	preflightDone := previous == nil
	if preflightDone {
		Report(ctx, WorkEvent{Phase: "preflight", Destination: d.Name})
		log("正在预检查 rsync：" + d.Name)
		if err := DryRun(ctx, p, d, selectedEntries, nil, rsync); err != nil {
			return nil, err
		}
	}
	unlock, err := LockRepository(d, true, true)
	if err != nil {
		return nil, err
	}
	defer unlock()
	repo, root, err := OpenRepository(d, true)
	if err != nil {
		return nil, err
	}
	planRoot := filepath.Join(root, "plans", p.ID)
	if err = SafeTree(root, planRoot); err != nil {
		return nil, err
	}
	for _, part := range []string{"staging", "snapshots"} {
		if err = SafeTree(root, filepath.Join(planRoot, part)); err != nil {
			return nil, err
		}
	}
	id := ID("snap-")
	stage := filepath.Join(planRoot, "staging", id)
	// Incomplete trees are kept for diagnostics and never advertised as snapshots.
	actualMount, err := mountPoint(filepath.Dir(root))
	if err != nil {
		return nil, err
	}
	s := Snapshot{UpdatedSourceIDs: append([]string(nil), p.ExecutionSourceIDs...), Mode: p.Mode, MountPath: actualMount, VolumeRelativePath: d.VolumeRelativePath, ExtendedMetadata: rsync.Modern, ID: id, PlanID: p.ID, PlanName: p.Name, DestinationID: d.ID, DestinationName: d.Name, DestinationPath: filepath.Dir(root), VolumeID: d.VolumeID, RepositoryID: repo.ID, CreatedAt: time.Now().UTC(), Entries: entries, Sources: []Source{}}
	config := p
	if p.RecoveryPlan != nil {
		config = *p.RecoveryPlan
	}
	// A JSON roundtrip drops all runtime-only scopes/approvals and deep-copies
	// slices so later view composition cannot modify the saved configuration.
	configData, configErr := json.Marshal(config)
	if configErr != nil {
		return nil, configErr
	}
	var saved Plan
	if err = json.Unmarshal(configData, &saved); err != nil {
		return nil, err
	}
	s.PlanConfig = &saved
	for _, src := range p.Sources {
		if src.Enabled {
			s.Sources = append(s.Sources, src)
		}
	}
	if expected, ok := p.ExpectedBaselineDigests[d.ID]; ok {
		actual := ""
		if previous != nil {
			actual = SnapshotDigest(*previous)
		}
		if expected != actual {
			return nil, errors.New("目标基线清单已变化，请重新预览")
		}
	}
	if previous != nil {
		if previous.RepositoryID != repo.ID {
			return nil, errors.New("备份仓库已被替换")
		}
		actual, e := ValidateSnapshot(*previous)
		if e != nil {
			return nil, e
		}
		copy := *previous
		copy.Path = actual
		copy.DestinationPath = filepath.Dir(root)
		copy.MountPath = actualMount
		previous = &copy

	}
	prevEntries := []Entry{}
	if previous != nil {
		prevEntries = previous.Entries
	}
	if p.ExpectedBaselines != nil {
		expected := p.ExpectedBaselines[d.ID]
		actual := ""
		if previous != nil {
			actual = previous.ID
		}
		if expected != actual {
			return nil, errors.New("目标基线已变化，请重新预览")
		}
	}
	if err = checkCurrentBaseline(p, d); err != nil {
		return nil, err
	}
	view := BuildView(p, selectedEntries, previous)
	if p.ExpectedViewDigests != nil && p.ExpectedViewDigests[d.ID] != ViewDigest(view) {
		return nil, ErrViewChanged
	}
	if p.Mode == "mirror" && len(view.Removed) > 0 && !p.MirrorDeletesAuthorized {
		return nil, errors.New("镜像删除未经预览确认")
	}

	// Decide before creating any staging tree, receipt, reference or manifest.
	unchanged, err := unchangedView(p, d, view, previous, rsync.Modern)
	if err != nil {
		return nil, err
	}
	if unchanged {
		Report(ctx, WorkEvent{Phase: "verify", Destination: d.Name})
		log("内容未变化，正在完整校验源文件与已有恢复点")
		if err = verifyUnchanged(ctx, p, d, selectedEntries, *previous, log); err != nil {
			return nil, err
		}
		log("内容未变化，完整校验通过；沿用恢复点 " + previous.ID)
		return nil, nil
	}
	if previous != nil && p.Mode != "snapshot" {
		log("正在完整校验目标基线内容")
		if err = AuditSnapshot(ctx, *previous); err != nil {
			return nil, fmt.Errorf("目标基线完整性校验失败: %w", err)
		}
	}
	for _, part := range []string{"staging", "snapshots"} {
		if err = os.MkdirAll(filepath.Join(planRoot, part), 0700); err != nil {
			return nil, err
		}
	}
	if err = SafeTree(root, stage); err != nil {
		return nil, err
	}
	if err = os.Mkdir(stage, 0700); err != nil {
		return nil, err
	}

	caps, er := ProbeFilesystemNames(stage)
	if er != nil {
		return nil, er
	}
	if er = ValidateViewNames(view, caps); er != nil {
		return nil, er
	}
	entries = view.Entries
	if !rsync.Modern {
		for i := range entries {
			entries[i].BasicMetadata = true
		}
	}
	view.Entries = entries
	binding := stagingBinding(p, d, repo.ID, rsync.Modern)
	if err = saveStagingReceipt(filepath.Dir(stage), id, binding); err != nil {
		return nil, err
	}
	if err = createViewDirectories(stage, view); err != nil {
		return nil, err
	}
	s.Entries = entries
	s.Sources = view.Sources
	if previous != nil {
		if err = seedRetained(ctx, stage, view, *previous, rsync.Modern); err != nil {
			return nil, err
		}
	}
	preview := Compare(selectedEntries, prevEntries, d.ID)
	var free syscall.Statfs_t
	if err = syscall.Statfs(filepath.Dir(root), &free); err != nil {
		return nil, err
	}
	if uint64(preview.TransferBytes)+16*1024*1024 > free.Bavail*uint64(free.Bsize) {
		return nil, errors.New("备份磁盘剩余空间不足")
	}
	reused, err := reuseSelectedFiles(ctx, p, stage, selectedEntries, previous, rsync.Modern)
	if err != nil {
		return nil, err
	}
	if len(reused) > 0 {
		log(fmt.Sprintf("直接复用候选文件: %d 个（发布前统一完整校验）", len(reused)))
	}
	// Prefer committed history over mutable interrupted work. Otherwise a failed
	// preflight could make the retry clone/copy thousands of already reusable files.
	recovered, err := recoverStagedFiles(ctx, filepath.Dir(stage), stage, binding, p, selectedEntries, rsync.Modern, reused, log)
	if err != nil {
		return nil, err
	}
	// Derive one allowlist after hardlink/copy recovery decisions. Link failures
	// stay in this list, so every file rsync may transfer receives fresh preflight.
	// Private staging may exist on failure, but no generation/current is published.
	transfers := []Entry{}
	for _, en := range selectedEntries {
		if en.Kind != "directory" && !recovered[entryKey(en)] && !reused[entryKey(en)] {
			transfers = append(transfers, en)
		}
	}
	if !preflightDone && len(transfers) > 0 {
		Report(ctx, WorkEvent{Phase: "preflight", Destination: d.Name})
		log(fmt.Sprintf("正在预检查 rsync 实际传输清单：%s，%d 个路径", d.Name, len(transfers)))
		if err = DryRun(ctx, p, d, transfers, nil, rsync); err != nil {
			return nil, err
		}
	}
	for _, src := range p.Sources {
		if !src.Enabled {
			continue
		}
		if err = Checkpoint(ctx); err != nil {
			return nil, err
		}
		if _, err = BindSource(src); err != nil {
			return nil, err
		}
		log("正在备份 " + src.Name + " → " + d.Name)
		dest := filepath.Join(stage, src.ID)
		if err = os.MkdirAll(dest, 0700); err != nil {
			return nil, err
		}
		var files bytes.Buffer
		for _, en := range transfers {
			if en.SourceID != src.ID || en.Kind == "directory" {
				continue
			}
			files.WriteString(en.Path)
			files.WriteByte(0)
		}
		if files.Len() == 0 {
			if _, err = CheckDestination(d); err != nil {
				return nil, err
			}
			continue
		}
		list := filepath.Join(stage, ".files-"+src.ID)
		if err = os.WriteFile(list, files.Bytes(), 0600); err != nil {
			return nil, err
		}
		Report(ctx, WorkEvent{Phase: "prepare", Source: src.Name, Destination: d.Name})
		Report(ctx, WorkEvent{Phase: "transfer", Source: src.Name, Destination: d.Name})
		args := append([]string{"--progress"}, Arguments(list, src.Path, dest, "", rsync.Modern)...)
		cmd := exec.CommandContext(ctx, rsync.Path, args...)
		var output limitedBuffer
		cmd.Stdout = &progressWriter{ctx: ctx}
		cmd.Stderr = &output
		err = RunCommand(ctx, cmd)
		os.Remove(list)
		if output.Len() > 0 {
			log(output.String())
		}
		if err != nil {
			return nil, fmt.Errorf("rsync %s: %w · %s", src.Name, err, output.String())
		}
		if _, err = CheckDestination(d); err != nil {
			return nil, err
		}
	}
	if previous != nil {
		if err = finalizeRetainedDirectories(stage, view, *previous, rsync.Modern); err != nil {
			return nil, err
		}
	}
	if err = finalizeSelectedDirectories(stage, p, selectedEntries, rsync.Modern); err != nil {
		return nil, err
	}
	if removed, e := cleanStagingFinderMetadata(ctx, stage, entries); e != nil {
		return nil, e
	} else if removed > 0 {
		log(fmt.Sprintf("已清理暂存区中 Finder 自动生成的 %d 个 .DS_Store 元数据文件", removed))
	}
	if err = VerifyTreeLayout(ctx, stage, entries, view.Sources, false); err != nil {
		return nil, err
	}
	Report(ctx, WorkEvent{Phase: "verify", Destination: d.Name})
	log("正在校验快照内容（SHA-256）")
	if err = Verify(ctx, stage, entries); err != nil {
		return nil, err
	}
	if rsync.Modern {
		if err = VerifyAttributes(stage, entries); err != nil {
			return nil, err
		}
	}
	if err = Checkpoint(ctx); err != nil {
		return nil, err
	}
	if _, err = CheckDestination(d); err != nil {
		return nil, err
	}
	for _, en := range entries {
		if en.Kind != "directory" {
			s.Files++
			s.Bytes += en.Size
		}
	}
	for _, src := range p.Sources {
		if src.Enabled {
			if _, err = BindSource(src); err != nil {
				return nil, err
			}
		}
	}
	s.Path = filepath.Join(planRoot, "snapshots", id)
	if p.Mode == "incremental" || p.Mode == "mirror" {
		s.CurrentPath = filepath.Join(planRoot, "current")
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, err
	}
	if err = writeAtomic(filepath.Join(stage, "manifest.json"), b); err != nil {
		return nil, err
	}
	if err = syncTree(stage); err != nil {
		return nil, err
	}
	if err = Checkpoint(ctx); err != nil {
		return nil, err
	}
	if err = os.Rename(stage, s.Path); err != nil {
		return nil, err
	}
	f, err := os.Open(filepath.Dir(s.Path))
	if err == nil {
		err = f.Sync()
		f.Close()
	}
	if err != nil {
		return nil, err
	}
	if s.CurrentPath != "" {
		if err = Checkpoint(ctx); err != nil {
			return nil, err
		}
		if err = checkCurrentBaseline(p, d); err != nil {
			return nil, err
		}
		if err = PublishCurrent(d, s); err != nil {
			return nil, err
		}
		log("同步视图已原子更新 · " + s.CurrentPath)
	}
	if e := os.Remove(receiptPath(filepath.Join(planRoot, "staging"), id)); e != nil && !os.IsNotExist(e) {
		log("快照已提交，暂存恢复标记待清理: " + e.Error())
	}
	log("快照已提交 · " + id)
	return &s, nil
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len() < 64*1024 {
		left := 64*1024 - b.Len()
		if len(p) > left {
			p = p[:left]
		}
		b.Buffer.Write(p)
	}
	return n, nil
}
func syncTree(root string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		f, e := os.Open(path)
		if e != nil {
			return e
		}
		defer f.Close()
		return f.Sync()
	})
}
func ValidateSnapshot(s Snapshot) (string, error) {
	root, _, err := readSnapshotManifest(s)
	return root, err
}

// Read and validate once; callers needing the complete manifest reuse this value.
func readSnapshotManifest(s Snapshot) (string, Snapshot, error) {
	d := Destination{Path: s.DestinationPath, VolumeID: s.VolumeID, MountPath: s.MountPath, VolumeRelativePath: s.VolumeRelativePath}
	repo, root, e := OpenRepository(d, false)
	if e != nil {
		return "", Snapshot{}, e
	}
	if repo.ID != s.RepositoryID {
		return "", Snapshot{}, errors.New("仓库身份不匹配")
	}
	expected := filepath.Join(root, "plans", s.PlanID, "snapshots", s.ID)
	if !ValidID(s.ID) || !ValidID(s.PlanID) || s.Path != filepath.Join(s.DestinationPath, "LocalBackup", "plans", s.PlanID, "snapshots", s.ID) {
		return "", Snapshot{}, errors.New("快照路径无效")
	}
	if e = SafeTree(root, expected); e != nil {
		return "", Snapshot{}, e
	}
	if e = SafeTree(root, filepath.Join(expected, "manifest.json")); e != nil {
		return "", Snapshot{}, e
	}
	b, e := ReadMetadataFile(filepath.Join(expected, "manifest.json"), 1<<30)
	if e != nil {
		return "", Snapshot{}, e
	}
	var m Snapshot
	if e = json.Unmarshal(b, &m); e != nil {
		return "", Snapshot{}, e
	}
	if m.ID != s.ID || m.RepositoryID != s.RepositoryID {
		return "", Snapshot{}, errors.New("快照清单身份不匹配")
	}
	return expected, m, nil
}

func VerifyAttributes(root string, entries []Entry) error {
	for _, en := range entries {
		if en.BasicMetadata {
			continue
		}
		h, e := AttributeHash(filepath.Join(root, en.SourceID, filepath.FromSlash(en.Path)))
		if e != nil {
			return e
		}
		if h != en.AttributeHash {
			return fmt.Errorf("扩展属性校验失败: %s", en.Path)
		}
	}
	return nil
}
