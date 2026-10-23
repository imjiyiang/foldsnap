package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSourceBindingRejectsReplacementAndAllowsOriginalRename(t *testing.T) {
	p := fixture(t)
	src := p.Sources[0]
	if !SourceBound(src) || src.DirectoryBirthTime == "" {
		t.Fatal("missing persistent root binding")
	}
	renamed := src.Path + "-renamed"
	if err := os.Rename(src.Path, renamed); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveSource(src); !errors.Is(err, ErrSourceUnavailable) {
		t.Fatal("renamed source must await rebind", err)
	}
	if err := os.Mkdir(src.Path, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src.Path, "one.txt"), []byte("another directory"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Scan(context.Background(), p); !errors.Is(err, ErrSourceChanged) {
		t.Fatal("scanned replacement source", err)
	}
	src.Path = renamed
	rebound, err := BindSource(src)
	if err != nil || rebound.ID != p.Sources[0].ID || !sameSourceIdentity(rebound, p.Sources[0]) {
		t.Fatalf("lost original identity: %+v %v", rebound, err)
	}
	p.Sources[0] = rebound
	if _, _, err = Scan(context.Background(), p); err != nil {
		t.Fatal(err)
	}
}
func TestSourceBindingRejectsWrongVolumeBirthAndRootSymlink(t *testing.T) {
	p := fixture(t)
	src := p.Sources[0]
	wrong := src
	wrong.VolumeID = "another-volume"
	if _, err := ResolveSource(wrong); !errors.Is(err, ErrSourceUnavailable) {
		t.Fatal("accepted wrong volume", err)
	}
	wrong = src
	wrong.DirectoryBirthTime = "1"
	if _, err := ResolveSource(wrong); !errors.Is(err, ErrSourceChanged) {
		t.Fatal("accepted reused directory ID", err)
	}
	link := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(src.Path, link); err != nil {
		t.Fatal(err)
	}
	wrong = src
	wrong.Path = link
	if _, err := BindSource(wrong); !errors.Is(err, ErrSourceChanged) {
		t.Fatal("followed root symlink", err)
	}
}
func TestSourceBindingBlocksMovedRootInsideTarget(t *testing.T) {
	p := fixture(t)
	moved := filepath.Join(p.Destinations[0].Path, "source-moved-into-target")
	if err := os.Rename(p.Sources[0].Path, moved); err != nil {
		t.Fatal(err)
	}
	p.Sources[0].Path = moved
	if _, err := ResolveSources(p); err == nil {
		t.Fatal("allowed relocated source within target")
	}
}
func TestSourceBindingsCannotAuthorizeOfflineNewSource(t *testing.T) {
	p := fixture(t)
	old := p
	p.Sources = append([]Source{}, p.Sources...)
	p.Sources[0].Path = filepath.Join(t.TempDir(), "not-there")
	if err := ValidateForUpdate(&p, &old); err == nil {
		t.Fatal("allowed a different offline path")
	}
	p = old
	p.Sources = append([]Source{}, old.Sources...)
	os.Rename(p.Sources[0].Path, p.Sources[0].Path+"-offline")
	p.Sources[0].Enabled = false
	if err := ValidateForUpdate(&p, &old); err != nil {
		t.Fatal("cannot pause already bound offline source", err)
	}
	var noPrior *Plan
	if err := ValidateForUpdate(&p, noPrior); err == nil {
		t.Fatal("untrusted binding authorized offline new source")
	}
}

func TestSourceMountRenameResolvesByVolumeAndDirectoryIdentity(t *testing.T) {
	root := t.TempDir()
	oldMount := filepath.Join(root, "old-label")
	newMount := filepath.Join(root, "new-label")
	os.Mkdir(oldMount, 0755)
	os.Mkdir(filepath.Join(oldMount, "Documents"), 0755)
	original, err := BindSource(Source{ID: "src-portable", Name: "Portable", Path: filepath.Join(oldMount, "Documents"), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	original.MountPath = oldMount
	original.VolumeRelativePath = "Documents"
	os.Rename(oldMount, newMount)
	// A different directory with the same relative name is deliberately first.
	wrong := filepath.Join(root, "same-name-other-directory")
	os.Mkdir(wrong, 0755)
	os.Mkdir(filepath.Join(wrong, "Documents"), 0755)
	_, missing := BindSource(original)
	resolved, err := resolveSourceAtMounts(original, []string{wrong, newMount}, missing)
	expected, _ := Canonical(filepath.Join(newMount, "Documents"))
	if err != nil || resolved.Path != expected || !sameSourceIdentity(resolved, original) {
		t.Fatalf("mount rename resolution failed: %+v %v", resolved, err)
	}
	if _, err = resolveSourceAtMounts(original, []string{wrong}, missing); err == nil {
		t.Fatal("matched a different directory by label")
	}
	changedVolume := original
	changedVolume.VolumeID = "other-uuid"
	if _, err = resolveSourceAtMounts(changedVolume, []string{newMount}, missing); err == nil {
		t.Fatal("ignored expected volume UUID")
	}
}
