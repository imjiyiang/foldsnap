package backup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type diskPin struct {
	Version      int    `json:"version"`
	RepositoryID string `json:"repositoryId"`
	PlanID       string `json:"planId"`
	SnapshotID   string `json:"snapshotId"`
	Pinned       bool   `json:"pinned"`
}

// Mutable pin metadata is separate from the immutable snapshot manifest.
func SaveSnapshotPin(s Snapshot, pinned bool) error {
	d := Destination{ID: s.DestinationID, Path: s.DestinationPath, VolumeID: s.VolumeID, MountPath: s.MountPath, VolumeRelativePath: s.VolumeRelativePath}
	unlock, err := LockRepository(d, true, true)
	if err != nil {
		return err
	}
	defer unlock()
	if _, err = ValidateSnapshot(s); err != nil {
		return err
	}
	repo, root, err := OpenRepository(d, false)
	if err != nil {
		return err
	}
	if repo.ID != s.RepositoryID {
		return fmt.Errorf("仓库身份已变化")
	}
	dir := filepath.Join(root, "metadata", "pins")
	if err = SafeTree(root, dir); err != nil {
		return err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	path := filepath.Join(dir, s.ID+".json")
	if err = SafeTree(root, path); err != nil {
		return err
	}
	b, err := json.Marshal(diskPin{1, repo.ID, s.PlanID, s.ID, pinned})
	if err != nil {
		return err
	}
	if err = writeAtomic(path, b); err != nil {
		return err
	}
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// Unknown/corrupt pin state must never silently weaken imported protection.
// Callers choose conservative pinning and surface the uncertainty in preview.
func ReadSnapshotPin(root string, s Snapshot) (bool, bool, error) {
	path := filepath.Join(root, "metadata", "pins", s.ID+".json")
	if err := SafeTree(root, path); err != nil {
		return false, false, err
	}
	b, err := ReadMetadataFile(path, 4096)
	if os.IsNotExist(err) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	var p diskPin
	if len(b) > 4096 || json.Unmarshal(b, &p) != nil || p.Version != 1 || p.RepositoryID != s.RepositoryID || p.PlanID != s.PlanID || p.SnapshotID != s.ID {
		return false, false, fmt.Errorf("快照保留标记无效: %s", s.ID)
	}
	return p.Pinned, true, nil
}
