package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type RestoreRequest struct {
	SnapshotID  string `json:"snapshotId"`
	SourceID    string `json:"sourceId"`
	Path        string `json:"path"`
	Destination string `json:"destination"`
}

func Restore(ctx context.Context, s Snapshot, r RestoreRequest, plans []Plan, rsync RsyncInfo) (string, error) {
	return RestoreWithOutputAudit(ctx, s, r, plans, rsync, nil)
}

// RestoreWithOutputAudit durably records the intended output before creating it.
// A recorded path may not exist if interrupted before mkdir; it is never proof
// of successful restoration. Failure to record aborts without creating output.
func RestoreWithOutputAudit(ctx context.Context, s Snapshot, r RestoreRequest, plans []Plan, rsync RsyncInfo, recordOutput func(string) error) (result string, resultErr error) {
	created := ""
	defer func() {
		if resultErr != nil && created != "" {
			result = created
			resultErr = fmt.Errorf("%w; output: %s", resultErr, created)
		}
	}()
	unlock, e := LockRepository(Destination{ID: s.DestinationID, Path: s.DestinationPath, VolumeID: s.VolumeID, MountPath: s.MountPath, VolumeRelativePath: s.VolumeRelativePath}, false, false)
	if e != nil {
		return "", e
	}
	defer unlock()
	root, e := ValidateSnapshot(s)
	if e != nil {
		return "", e
	}
	if !ValidID(r.SourceID) {
		return "", errors.New("无效的源目录")
	}
	var found bool
	for _, src := range s.Sources {
		if src.ID == r.SourceID {
			found = true
		}
	}
	if !found {
		return "", errors.New("快照中不存在这个源目录")
	}
	relative := r.Path
	if relative == "" {
		relative = "."
	}
	if relative != "." && !SafeRelative(relative) {
		return "", errors.New("恢复路径无效")
	}
	source := filepath.Join(root, r.SourceID, relative)
	if e = SafeTree(root, filepath.Dir(source)); e != nil {
		return "", e
	}
	st, e := os.Lstat(source)
	if e != nil {
		return "", e
	}
	parent, e := Canonical(r.Destination)
	if e != nil {
		return "", e
	}
	stp, e := os.Stat(parent)
	if e != nil || !stp.IsDir() {
		return "", errors.New("恢复目标必须是已存在的目录")
	}
	for _, src := range s.Sources {
		if Contains(src.Path, parent) {
			return "", errors.New("请恢复到原始源目录之外")
		}
	}
	for _, p := range plans {
		for _, src := range p.Sources {
			if Contains(src.Path, parent) {
				return "", errors.New("请恢复到源目录之外的新位置")
			}
		}
		for _, d := range p.Destinations {
			if Contains(d.Path, parent) {
				return "", errors.New("不能恢复到备份仓库内")
			}
		}
	}
	if Contains(s.DestinationPath, parent) {
		return "", errors.New("不能恢复到备份仓库内")
	}

	selected := []Entry{}
	for _, en := range s.Entries {
		if en.SourceID == r.SourceID && (relative == "." || Contains(relative, filepath.FromSlash(en.Path))) {
			selected = append(selected, en)
		}
	}
	if relative != "." {
		listed := false
		for _, en := range selected {
			if filepath.FromSlash(en.Path) == relative {
				listed = true
				break
			}
		}
		if !listed {
			return "", errors.New("恢复路径未登记在快照清单中")
		}
	}
	if e = Verify(ctx, root, selected); e != nil {
		return "", fmt.Errorf("历史内容校验失败: %w", e)
	}
	if s.ExtendedMetadata {
		if e = VerifyAttributes(root, selected); e != nil {
			return "", e
		}
	}
	sourceRoot := source
	if !st.IsDir() {
		sourceRoot = filepath.Dir(source)
	}
	var list bytes.Buffer
	outputPaths := make([]string, 0, len(selected))
	for _, en := range selected {
		rel := filepath.FromSlash(en.Path)
		if relative != "." {
			if st.IsDir() {
				rel, _ = filepath.Rel(relative, rel)
			} else {
				rel = filepath.Base(rel)
			}
		}
		if rel != "." && !SafeRelative(rel) {
			return "", errors.New("恢复清单路径无效")
		}
		if en.Kind != "directory" {
			list.WriteString(filepath.ToSlash(rel))
			list.WriteByte(0)
		}
		outputPaths = append(outputPaths, rel)
	}
	manifest, e := os.CreateTemp("", "localbackup-restore-files-")
	if e != nil {
		return "", e
	}
	listPath := manifest.Name()
	defer os.Remove(listPath)
	if _, e = manifest.Write(list.Bytes()); e != nil {
		manifest.Close()
		return "", e
	}
	if e = manifest.Close(); e != nil {
		return "", e
	}
	dest := filepath.Join(parent, ID("LocalBackup-Restore-"))
	if err := Checkpoint(ctx); err != nil {
		return "", err
	}
	if recordOutput != nil {
		if err := recordOutput(dest); err != nil {
			return "", fmt.Errorf("无法记录恢复输出位置: %w", err)
		}
	}
	if err := Checkpoint(ctx); err != nil {
		return "", err
	}
	if e = os.Mkdir(dest, 0700); e != nil {
		return "", e
	}
	created = dest
	for i, en := range selected {
		if en.Kind == "directory" {
			if e = os.MkdirAll(filepath.Join(dest, outputPaths[i]), 0700); e != nil {
				return "", e
			}
		}
	}
	args := Arguments(listPath, sourceRoot, dest, "", rsync.Modern)
	var out limitedBuffer
	cmd := exec.CommandContext(ctx, rsync.Path, args...)
	cmd.Stdout = &out
	cmd.Stderr = &out
	if list.Len() > 0 {
		if e = RunCommand(ctx, cmd); e != nil {
			return "", fmt.Errorf("恢复失败，未覆盖原文件: %w %s", e, out.String())
		}
	}
	for i := len(selected) - 1; i >= 0; i-- {
		en := selected[i]
		if en.Kind != "directory" {
			continue
		}
		to := filepath.Join(dest, outputPaths[i])
		if rsync.Modern && s.ExtendedMetadata && !en.BasicMetadata {
			from := filepath.Join(root, en.SourceID, filepath.FromSlash(en.Path))
			if e = CopyAttributes(from, to); e != nil {
				return "", e
			}
		}
		if e = os.Chmod(to, os.FileMode(en.Mode)); e != nil {
			return "", e
		}
		at := time.Unix(0, en.ModTime)
		if e = os.Chtimes(to, at, at); e != nil {
			return "", e
		}
	}
	for i, en := range selected {
		path := filepath.Join(dest, outputPaths[i])
		if e = SafeTree(dest, filepath.Dir(path)); e != nil && outputPaths[i] != "." {
			return "", e
		}
		actual, err := os.Lstat(path)
		if err != nil {
			return "", err
		}
		switch en.Kind {
		case "file":
			if !actual.Mode().IsRegular() {
				return "", errors.New("恢复文件类型校验失败")
			}
			hash, err := HashFile(ctx, path)
			if err != nil || hash != en.Hash {
				return "", errors.New("恢复内容校验失败，已保留输出供检查")
			}
			if actual.ModTime().Unix() != time.Unix(0, en.ModTime).Unix() {
				return "", errors.New("恢复文件时间校验失败")
			}
		case "directory":
			if !actual.IsDir() {
				return "", errors.New("恢复目录类型校验失败")
			}
		case "symlink":
			link, err := os.Readlink(path)
			if err != nil || link != en.Link {
				return "", errors.New("恢复符号链接校验失败")
			}
		default:
			return "", errors.New("恢复类型不受支持")
		}
		if en.Kind != "symlink" && uint32(actual.Mode().Perm()) != en.Mode {
			return "", errors.New("恢复权限校验失败")
		}
		if rsync.Modern && s.ExtendedMetadata && !en.BasicMetadata {
			attrs, err := AttributeHash(path)
			if err != nil || attrs != en.AttributeHash {
				return "", errors.New("恢复扩展属性校验失败")
			}
		}
	}
	expected := map[string]bool{}
	for _, rel := range outputPaths {
		expected[filepath.Clean(rel)] = true
	}
	if e = filepath.WalkDir(dest, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = Checkpoint(ctx); err != nil {
			return err
		}
		if path == dest {
			return nil
		}
		rel, _ := filepath.Rel(dest, path)
		if !expected[rel] {
			return fmt.Errorf("恢复结果出现清单外路径: %s", rel)
		}
		return nil
	}); e != nil {
		return "", e
	}
	return dest, nil
}
