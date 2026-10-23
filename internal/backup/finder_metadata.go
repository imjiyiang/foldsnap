package backup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// Finder may create .DS_Store in a staging folder while a user browses a disk.
// Remove only that unselected, regular metadata file in our new mutable stage.
// Committed snapshots and manifest-selected .DS_Store files remain untouched.
func cleanStagingFinderMetadata(ctx context.Context, stage string, entries []Entry) (int, error) {
	selected := map[string]bool{}
	for _, entry := range entries {
		selected[filepath.Join(entry.SourceID, filepath.FromSlash(entry.Path))] = true
	}
	removed := 0
	err := filepath.WalkDir(stage, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = Checkpoint(ctx); err != nil {
			return err
		}
		if path == stage || d.Name() != ".DS_Store" {
			return nil
		}
		rel, err := filepath.Rel(stage, path)
		if err != nil || selected[rel] {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("暂存区中未预期的 .DS_Store 不是普通文件: %s", rel)
		}
		if err = SafeTree(stage, filepath.Dir(path)); err != nil {
			return err
		}
		if err = os.Remove(path); err != nil {
			return err
		}
		removed++
		return nil
	})
	return removed, err
}
