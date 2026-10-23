package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// View keeps the immutable restore tree separate from the live-source transfer
// list. Incremental synchronization may retain entries absent from that list.
type View struct {
	Entries  []Entry
	Retained []Entry
	Sources  []Source
	Removed  []Operation
}

func SelectedPath(p Plan, src Source, relative string) bool {
	path := filepath.Join(src.Path, filepath.FromSlash(relative))
	for _, excluded := range p.RuntimeExcludes {
		if Contains(excluded, path) {
			return false
		}
	}
	selector, e := CompileSelection(p, src)
	if e != nil {
		return false
	}
	if !selector.Possible(relative) {
		return false
	}
	return true
}
func splitPath(path string) []string {
	parts := []string{}
	for path != "." && path != "" {
		parent, name := filepath.Split(filepath.Clean(path))
		parts = append([]string{name}, parts...)
		path = filepath.Clean(parent)
	}
	return parts
}
func BuildView(p Plan, selected []Entry, previous *Snapshot) View {
	p.EvaluatedAt = EvaluationTime(p)
	v := View{Entries: append([]Entry{}, selected...), Retained: []Entry{}, Sources: []Source{}, Removed: []Operation{}}
	active := map[string]Source{}
	selectors := map[string]Selection{}
	sources := map[string]bool{}
	for _, src := range p.Sources {
		if src.Enabled {
			active[src.ID] = src
			selectors[src.ID], _ = CompileSelection(p, src)
			sources[src.ID] = true
			v.Sources = append(v.Sources, src)
		}
	}
	if previous == nil {
		return v
	}
	current := map[string]Entry{}
	for _, en := range selected {
		current[entryKey(en)] = en
	}
	if p.Mode == "incremental" || p.Mode == "mirror" || len(p.ExecutionSourceIDs) > 0 {
		for _, old := range previous.Entries {
			if _, ok := current[entryKey(old)]; ok {
				continue
			}
			src, enabled := active[old.SourceID]
			keep := !enabled || p.Mode == "incremental" || (p.Mode == "mirror" && !selectedOldEntry(p, src, old, selectors[src.ID]))
			if !keep {
				continue
			}
			conflict := false
			for parent := filepath.Dir(filepath.FromSlash(old.Path)); parent != "."; parent = filepath.Dir(parent) {
				if en, ok := current[old.SourceID+"/"+filepath.ToSlash(parent)]; ok && en.Kind != "directory" {
					conflict = true
					break
				}
			}
			if old.Kind != "directory" {
				for _, en := range selected {
					if en.SourceID == old.SourceID && en.Path != old.Path && Contains(filepath.FromSlash(old.Path), filepath.FromSlash(en.Path)) {
						conflict = true
						break
					}
				}
			}
			if conflict {
				continue
			}
			copy := old
			if !previous.ExtendedMetadata {
				copy.BasicMetadata = true
			}
			v.Retained = append(v.Retained, copy)
			v.Entries = append(v.Entries, copy)
			current[entryKey(copy)] = copy
		}
		// A protected file may outlive its deleted parent directory. Preserve
		// the immutable parent metadata as well so the retained tree is complete.
		oldDirectories := map[string]Entry{}
		for _, en := range previous.Entries {
			if en.Kind == "directory" {
				oldDirectories[entryKey(en)] = en
			}
		}
		retainedFiles := append([]Entry{}, v.Retained...)
		for _, en := range retainedFiles {
			for parent := filepath.Dir(filepath.FromSlash(en.Path)); parent != "."; parent = filepath.Dir(parent) {
				key := en.SourceID + "/" + filepath.ToSlash(parent)
				if _, ok := current[key]; ok {
					continue
				}
				if old, ok := oldDirectories[key]; ok {
					if !previous.ExtendedMetadata {
						old.BasicMetadata = true
					}
					v.Retained = append(v.Retained, old)
					v.Entries = append(v.Entries, old)
					current[key] = old
				}
			}
		}
		for _, src := range previous.Sources {
			if !sources[src.ID] {
				src.Enabled = false
				v.Sources = append(v.Sources, src)
				sources[src.ID] = true
			}
		}
	}
	final := map[string]Entry{}
	for _, en := range v.Entries {
		final[entryKey(en)] = en
	}
	for _, old := range previous.Entries {
		en, ok := final[entryKey(old)]
		if !ok || en.Kind != old.Kind {
			v.Removed = append(v.Removed, Operation{SourceID: old.SourceID, Path: old.Path, Type: "deleted", Size: old.Size, Reason: "从新的同步视图中移除；已完成的历史版本保留"})
		}
	}
	sort.Slice(v.Entries, func(i, j int) bool { return entryKey(v.Entries[i]) < entryKey(v.Entries[j]) })
	sort.Slice(v.Retained, func(i, j int) bool { return entryKey(v.Retained[i]) < entryKey(v.Retained[j]) })
	return v
}
func DescribeView(v View, previous *Snapshot, destination, mode string) Preview {
	old := []Entry{}
	if previous != nil {
		old = previous.Entries
	}
	out := Compare(v.Entries, old, destination)
	out.Removals = []Operation{}
	before := map[string]Entry{}
	after := map[string]Entry{}
	for _, en := range old {
		before[entryKey(en)] = en
	}
	for _, en := range v.Entries {
		after[entryKey(en)] = en
		if en.Kind == "directory" {
			previous, ok := before[entryKey(en)]
			kind := ""
			if !ok {
				out.Added++
				kind = "added"
			} else if previous.Kind != "directory" || previous.Mode != en.Mode {
				out.Modified++
				kind = "replaced"
			}
			if kind != "" {
				out.Operations = append(out.Operations, Operation{SourceID: en.SourceID, DestinationID: destination, Path: en.Path, Type: kind, Reason: "文件夹结构或权限变化"})
			}
		}
	}
	for _, en := range old {
		if en.Kind == "directory" {
			if _, ok := after[entryKey(en)]; !ok {
				out.Deleted++
				out.Operations = append(out.Operations, Operation{SourceID: en.SourceID, DestinationID: destination, Path: en.Path, Type: "deleted", Reason: "从新的视图移除文件夹；历史版本保留"})
			}
		}
	}

	for i := range out.Operations {
		op := &out.Operations[i]
		if prev, ok := before[op.SourceID+"/"+op.Path]; ok {
			if next, ok := after[op.SourceID+"/"+op.Path]; ok && prev.Kind != next.Kind {
				op.Type = "replaced"
				op.Reason = prev.Kind + " → " + next.Kind + "，旧版本保留"
			}
		}
	}
	for _, en := range v.Retained {
		if en.Kind == "directory" {
			continue
		}
		out.Retained++
		out.RetainedBytes += en.Size
		reason := "增量模式保留旧路径"
		if mode == "snapshot" {
			reason = "目录此次未选择，沿用此前备份"
		}
		if mode == "mirror" {
			reason = "排除项或未启用源受保护，保留旧路径"
		}
		out.Operations = append(out.Operations, Operation{SourceID: en.SourceID, DestinationID: destination, Path: en.Path, Type: "retained", Size: en.Size, Reason: reason})
	}
	if mode == "mirror" {
		for _, op := range v.Removed {
			op.DestinationID = destination
			out.Removals = append(out.Removals, op)
			out.DeletePaths++
			out.DeleteBytes += op.Size
		}
	}
	return out
}

func ReadCurrent(d Destination, planID string) (*Snapshot, error) {
	_, root, e := OpenRepository(d, false)
	if os.IsNotExist(e) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	path := filepath.Join(root, "plans", planID, "current")
	if e = SafeTree(root, filepath.Dir(path)); e != nil {
		return nil, e
	}
	link, e := os.Readlink(path)
	if os.IsNotExist(e) {
		return nil, nil
	}
	if e != nil {
		return nil, errors.New("同步视图必须是由 LocalBackup 管理的链接")
	}
	if !SafeRelative(link) || filepath.Dir(link) != "snapshots" || !ValidID(filepath.Base(link)) {
		return nil, errors.New("同步视图链接无效")
	}
	manifest := filepath.Join(filepath.Dir(path), link, "manifest.json")
	if e = SafeTree(root, manifest); e != nil {
		return nil, e
	}
	b, e := ReadMetadataFile(manifest, 1<<30)
	if e != nil {
		return nil, e
	}
	var sn Snapshot
	if e = json.Unmarshal(b, &sn); e != nil {
		return nil, e
	}
	if sn.PlanID != planID || sn.DestinationID != d.ID || sn.ID != filepath.Base(link) {
		return nil, errors.New("同步视图与计划/目标身份不匹配")
	}
	if _, e = ValidateSnapshot(sn); e != nil {
		return nil, e
	}
	return &sn, nil
}
func PublishCurrent(d Destination, s Snapshot) error {
	_, root, e := OpenRepository(d, false)
	if e != nil {
		return e
	}
	if _, e = ValidateSnapshot(s); e != nil {
		return e
	}
	base := filepath.Join(root, "plans", s.PlanID)
	if e = SafeTree(root, base); e != nil {
		return e
	}
	path := filepath.Join(base, "current")
	if st, e := os.Lstat(path); e == nil && st.Mode()&os.ModeSymlink == 0 {
		return errors.New("同步视图被非链接文件占用，未覆盖")
	}
	temp := filepath.Join(base, ID(".current-"))
	if e = os.Symlink(filepath.Join("snapshots", s.ID), temp); e != nil {
		return e
	}
	defer os.Remove(temp)
	if e = os.Rename(temp, path); e != nil {
		return e
	}
	f, e := os.Open(base)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func AuditSnapshot(ctx context.Context, s Snapshot) error {
	root, e := ValidateSnapshot(s)
	if e != nil {
		return e
	}
	return auditSnapshotContents(ctx, s, root)
}

// Caller has freshly validated root and its manifest. This helper never escapes
// the audit checkpoint or caches validation across runs.
func auditSnapshotContents(ctx context.Context, s Snapshot, root string) error {
	var e error
	if e = Verify(ctx, root, s.Entries); e != nil {
		return e
	}
	if s.ExtendedMetadata {
		if e = VerifyAttributes(root, s.Entries); e != nil {
			return e
		}
	}
	known := map[string]bool{"manifest.json": true}
	for _, src := range s.Sources {
		known[src.ID] = true
	}
	for _, en := range s.Entries {
		known[filepath.Join(en.SourceID, filepath.FromSlash(en.Path))] = true
	}
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = Checkpoint(ctx); err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if !known[rel] {
			return fmt.Errorf("同步视图被外部修改，发现未登记路径: %s", rel)
		}
		return nil
	})
}

func seedRetained(ctx context.Context, stage string, v View, previous Snapshot, modern bool) error {
	root, e := ValidateSnapshot(previous)
	if e != nil {
		return e
	}
	for _, src := range v.Sources {
		if e = os.MkdirAll(filepath.Join(stage, src.ID), 0700); e != nil {
			return e
		}
	}
	for _, en := range v.Retained {
		if e = Checkpoint(ctx); e != nil {
			return e
		}
		from := filepath.Join(root, en.SourceID, filepath.FromSlash(en.Path))
		to := filepath.Join(stage, en.SourceID, filepath.FromSlash(en.Path))
		if e = SafeTree(root, filepath.Dir(from)); e != nil {
			return e
		}
		if e = os.MkdirAll(filepath.Dir(to), 0700); e != nil {
			return e
		}
		switch en.Kind {
		case "directory":
			e = os.MkdirAll(to, 0700)
		case "symlink":
			e = os.Symlink(en.Link, to)
			if e == nil && modern && !en.BasicMetadata {
				e = CopyAttributes(from, to)
			}
		case "file":
			e = os.Link(from, to)
			if e != nil {
				e = copyRetainedFile(ctx, from, to, en, modern)
			}
		default:
			return errors.New("保留项类型无效")
		}
		if e != nil {
			return e
		}
	}
	return nil
}
func copyRetainedFile(ctx context.Context, from, to string, en Entry, modern bool) error {
	descriptor, e := unix.Open(from, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if e != nil {
		return e
	}
	in := os.NewFile(uintptr(descriptor), from)
	defer in.Close()
	before, e := in.Stat()
	if e != nil {
		return e
	}
	if !before.Mode().IsRegular() {
		return fmt.Errorf("复制来源不是普通文件: %s", from)
	}
	out, e := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	buffer := make([]byte, 256*1024)
	for {
		if e = Checkpoint(ctx); e != nil {
			break
		}
		n, readErr := in.Read(buffer)
		if n > 0 {
			_, e = out.Write(buffer[:n])
			if e != nil {
				break
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			e = readErr
			break
		}
	}
	closeErr := out.Close()
	latest, statErr := os.Lstat(from)
	if e == nil && (statErr != nil || !samePhysicalVersion(before, latest)) {
		e = fmt.Errorf("复制期间文件身份发生变化: %s", from)
	}
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	if e = os.Chmod(to, os.FileMode(en.Mode)); e != nil {
		return e
	}
	at := time.Unix(0, en.ModTime)
	if e = os.Chtimes(to, at, at); e != nil {
		return e
	}
	if modern && !en.BasicMetadata {
		return CopyAttributes(from, to)
	}
	return nil
}
func finalizeRetainedDirectories(stage string, v View, previous Snapshot, modern bool) error {
	root, e := ValidateSnapshot(previous)
	if e != nil {
		return e
	}
	for i := len(v.Retained) - 1; i >= 0; i-- {
		en := v.Retained[i]
		if en.Kind != "directory" {
			continue
		}
		path := filepath.Join(stage, en.SourceID, filepath.FromSlash(en.Path))
		if modern && !en.BasicMetadata {
			if e = CopyAttributes(filepath.Join(root, en.SourceID, filepath.FromSlash(en.Path)), path); e != nil {
				return e
			}
		}
		if e = os.Chmod(path, os.FileMode(en.Mode)); e != nil {
			return e
		}
		at := time.Unix(0, en.ModTime)
		if e = os.Chtimes(path, at, at); e != nil {
			return e
		}
	}
	return nil
}

func EquivalentTrees(a, b []Entry) bool {
	if len(a) != len(b) {
		return false
	}
	before := map[string]Entry{}
	for _, en := range b {
		before[entryKey(en)] = en
	}
	for _, en := range a {
		old, ok := before[entryKey(en)]
		if !ok || !sameContent(en, old) || en.Mode != old.Mode {
			return false
		}
		if !en.BasicMetadata && !old.BasicMetadata && en.AttributeHash != old.AttributeHash {
			return false
		}
		if en.Kind == "file" && en.ModTime/1e9 != old.ModTime/1e9 {
			return false
		}
	}
	return true
}

func checkCurrentBaseline(p Plan, d Destination) error {
	if expected, ok := p.ExpectedCurrentIDs[d.ID]; ok {
		current, e := ReadCurrent(d, p.ID)
		if e != nil {
			return e
		}
		actual := ""
		if current != nil {
			actual = current.ID
		}
		if current != nil && p.ExpectedBaselineDigests != nil {
			if digest, ok := p.ExpectedBaselineDigests[d.ID]; ok && SnapshotDigest(*current) != digest {
				return errors.New("同步视图的清单已变化，请重新预览")
			}
		}
		if expected != actual {
			return errors.New("同步视图在执行期间已变化，未覆盖当前视图，请重新预览")
		}
	}
	return nil
}

func SnapshotDigest(s Snapshot) string {
	b, _ := json.Marshal(s)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func createViewDirectories(stage string, v View) error {
	for _, src := range v.Sources {
		if e := os.MkdirAll(filepath.Join(stage, src.ID), 0700); e != nil {
			return e
		}
	}
	for _, en := range v.Entries {
		if en.Kind == "directory" {
			if e := os.MkdirAll(filepath.Join(stage, en.SourceID, filepath.FromSlash(en.Path)), 0700); e != nil {
				return e
			}
		}
	}
	return nil
}
func finalizeSelectedDirectories(stage string, p Plan, entries []Entry, modern bool) error {
	sources := map[string]Source{}
	for _, s := range p.Sources {
		sources[s.ID] = s
	}
	for i := len(entries) - 1; i >= 0; i-- {
		en := entries[i]
		if en.Kind != "directory" {
			continue
		}
		src := sources[en.SourceID]
		from := filepath.Join(src.Path, filepath.FromSlash(en.Path))
		if e := SafeTree(src.Path, from); e != nil {
			return e
		}
		st, e := os.Lstat(from)
		if e != nil || !st.IsDir() {
			return fmt.Errorf("源目录类型发生变化: %s", en.Path)
		}
		to := filepath.Join(stage, en.SourceID, filepath.FromSlash(en.Path))
		if modern {
			if e = CopyAttributes(from, to); e != nil {
				return e
			}
		}
		if e = os.Chmod(to, os.FileMode(en.Mode)); e != nil {
			return e
		}
		at := time.Unix(0, en.ModTime)
		if e = os.Chtimes(to, at, at); e != nil {
			return e
		}
	}
	return nil
}
func VerifyTreeLayout(ctx context.Context, root string, entries []Entry, sources []Source, allowManifest bool) error {
	known := map[string]bool{}
	if allowManifest {
		known["manifest.json"] = true
	}
	for _, s := range sources {
		known[s.ID] = true
	}
	for _, en := range entries {
		known[filepath.Join(en.SourceID, filepath.FromSlash(en.Path))] = true
	}
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = Checkpoint(ctx); err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if !known[rel] {
			return fmt.Errorf("发现清单外路径，未确认完整性: %s", rel)
		}
		return nil
	})
}

func ViewDigest(v View) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
