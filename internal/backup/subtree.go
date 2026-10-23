package backup

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func reusableProjection(scope SourceScope, base *SourceProjection) bool {
	if base == nil || !scope.Cacheable || !base.Scope.Cacheable {
		return false
	}
	b := base.Scope
	age := time.Since(b.FullScanAt)
	return age >= 0 && age < time.Hour && b.Generation < scope.Generation && b.Root == scope.Root && b.RootIdentity == scope.RootIdentity && b.VolumeID == scope.VolumeID && b.Filesystem == scope.Filesystem && b.Policy == scope.Policy
}
func underScan(path string, roots []string) bool {
	for _, root := range roots {
		if path == root || strings.HasPrefix(path, root+"/") {
			return true
		}
	}
	return false
}

// Scan the parent of each dirty item. This covers disappearance and rename,
// and siblings created in the same event batch. Never descend through a link.
func dirtyParents(root string, hints []string, selector Selection) ([]string, bool, error) {
	selected := []string{}
	for _, hint := range hints {
		if hint == "." || !SafeRelative(hint) {
			return nil, false, nil
		}
		parent := filepath.ToSlash(filepath.Dir(filepath.Clean(filepath.FromSlash(hint))))
		if parent == "." {
			return nil, false, nil
		}
		current := ""
		for _, part := range strings.Split(parent, "/") {
			if current != "" {
				current += "/"
			}
			current += part
			path := filepath.Join(root, filepath.FromSlash(current))
			st, err := os.Lstat(path)
			if os.IsNotExist(err) {
				parent = current
				break
			}
			if err != nil {
				return nil, false, err
			}
			if !st.IsDir() {
				parent = current
				break
			}
			canonical, err := Canonical(path)
			if err != nil {
				return nil, false, err
			}
			if canonical != path {
				parent = filepath.ToSlash(filepath.Dir(current))
				if parent == "." {
					return nil, false, nil
				}
				break
			}
			include, _ := selector.Match(current, "directory", 0, st.ModTime())
			if !include {
				parent = current
				break
			}
		}
		selected = append(selected, parent)
	}
	sort.Strings(selected)
	roots := []string{}
	for _, candidate := range selected {
		if !underScan(candidate, roots) {
			roots = append(roots, candidate)
		}
	}
	return roots, true, nil
}
