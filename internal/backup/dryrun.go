package backup

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// DryRun validates manifest transfer arguments against an empty disposable target.
// Execution calls this before transfer. Previews use CheckRsyncCapabilities on
// fixed generated data instead; reuse eligibility is verified by Execute.
func DryRun(ctx context.Context, p Plan, d Destination, entries []Entry, previous *Snapshot, rsync RsyncInfo) error {
	if !rsync.Available {
		return fmt.Errorf("rsync 不可用")
	}
	tmp, e := os.MkdirTemp("", "localbackup-preview-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(tmp)
	if e = os.Mkdir(filepath.Join(tmp, "target"), 0700); e != nil {
		return e
	}
	// Prepare selected directories and files-from parents in disposable
	// namespaces. Rsync never receives a historical link-dest.
	for _, src := range p.Sources {
		if !ValidID(src.ID) {
			return fmt.Errorf("无效的源目录 ID")
		}
	}
	for _, en := range entries {
		if !ValidID(en.SourceID) || !SafeRelative(en.Path) {
			return fmt.Errorf("无效的预演路径")
		}
	}
	// A sparse allowlist need not include directory entries. Build only the
	// required disposable parents; no old backup or real target is touched.
	parents := map[string]bool{}
	for _, en := range entries {
		if en.Kind != "directory" {
			parent := filepath.Dir(filepath.Join(tmp, "target", en.SourceID, filepath.FromSlash(en.Path)))
			if parents[parent] {
				continue
			}
			if e = os.MkdirAll(parent, 0700); e != nil {
				return e
			}
			parents[parent] = true
		}
	}
	if e = createViewDirectories(filepath.Join(tmp, "target"), View{Sources: p.Sources, Entries: entries}); e != nil {
		return e
	}
	for _, src := range p.Sources {
		if !src.Enabled {
			continue
		}
		var files bytes.Buffer
		for _, en := range entries {
			if en.SourceID == src.ID && en.Kind != "directory" {
				files.WriteString(en.Path)
				files.WriteByte(0)
			}
		}
		if files.Len() == 0 {
			continue
		}
		list := filepath.Join(tmp, "files")
		if e = os.WriteFile(list, files.Bytes(), 0600); e != nil {
			return e
		}
		// Preview comparisons come from the indexed source and snapshot manifest.
		// A disposable empty destination checks rsync/filter compatibility without
		// hashing or linking the entire old backup. Execute verifies source and historical content before directly
		// linking eligible files into staging, then verifies the complete result.
		prior := ""
		args := append([]string{"--dry-run"}, Arguments(list, src.Path, filepath.Join(tmp, "target", src.ID), prior, rsync.Modern)...)
		var out limitedBuffer
		cmd := exec.CommandContext(ctx, rsync.Path, args...)
		cmd.Stdout = &out
		cmd.Stderr = &out
		if e = RunCommand(ctx, cmd); e != nil {
			return fmt.Errorf("rsync 预演 %s → %s: %w %s", src.Name, d.Name, e, out.String())
		}
	}
	return nil
}

// CheckRsyncCapabilities exercises the real transfer flags with fixed generated
// data only. It does not revisit user files or claim they remain readable after
// the source scan. Execution retains its separate full-manifest DryRun.
func CheckRsyncCapabilities(ctx context.Context, rsync RsyncInfo) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := Checkpoint(ctx); err != nil {
		return err
	}
	probe, err := os.MkdirTemp("", "localbackup-rsync-capabilities-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(probe)
	source := filepath.Join(probe, "source")
	if err = os.MkdirAll(filepath.Join(source, "nested"), 0700); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(source, "nested", "测试.txt"), []byte("LocalBackup capability probe\n"), 0600); err != nil {
		return err
	}
	if err = os.Symlink("nested/测试.txt", filepath.Join(source, "link")); err != nil {
		return err
	}
	p := Plan{Sources: []Source{{ID: "probe", Name: "工具兼容性测试", Path: source, Enabled: true}}}
	entries := []Entry{{SourceID: "probe", Path: "nested", Kind: "directory"}, {SourceID: "probe", Path: "nested/测试.txt", Kind: "file"}, {SourceID: "probe", Path: "link", Kind: "symlink", Link: "nested/测试.txt"}}
	return DryRun(ctx, p, Destination{Name: "临时兼容性测试目录"}, entries, nil, rsync)
}
