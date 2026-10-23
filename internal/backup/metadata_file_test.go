package backup

import (
	"bytes"
	"context"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"testing"
)

func TestMetadataReaderPreservesEmptyExactAndLargePayloads(t *testing.T) {
	file := filepath.Join(t.TempDir(), "manifest.json")
	for _, size := range []int{0, 1, 512, 256 * 1024, 2 * 1024 * 1024} {
		want := bytes.Repeat([]byte("x"), size)
		if err := os.WriteFile(file, want, 0600); err != nil {
			t.Fatal(err)
		}
		for _, limit := range []int64{int64(size), int64(size) + 100} {
			got, err := ReadMetadataFile(file, limit)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatal(size, limit, err)
			}
		}
	}
}

func TestMetadataReaderRejectsSpecialLinksAndOversize(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "data")
	os.WriteFile(file, []byte("12345"), 0600)
	if _, err := ReadMetadataFile(file, 4); err == nil {
		t.Fatal("oversized metadata accepted")
	}
	link := filepath.Join(root, "link")
	os.Symlink(file, link)
	if _, err := ReadMetadataFile(link, 10); err == nil {
		t.Fatal("metadata link followed")
	}
	fifo := filepath.Join(root, "fifo")
	if err := unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadMetadataFile(fifo, 10); err == nil {
		t.Fatal("blocking metadata accepted")
	}
}
func TestPinMetadataPreservesImmutableManifestAndSurvivesReload(t *testing.T) {
	p := fixture(t)
	write(t, filepath.Join(p.Sources[0].Path, "file.txt"), "data")
	rs := DetectRsync("")
	if !rs.Available {
		t.Skip("requires rsync")
	}
	entries, _, err := Scan(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	sn, err := Execute(context.Background(), p, p.Destinations[0], entries, nil, rs, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sn.Path, "manifest.json")
	before, _ := os.ReadFile(path)
	for _, want := range []bool{true, false} {
		if err = SaveSnapshotPin(*sn, want); err != nil {
			t.Fatal(err)
		}
		pinned, known, err := ReadSnapshotPin(filepath.Join(p.Destinations[0].Path, "LocalBackup"), *sn)
		if err != nil || !known || pinned != want {
			t.Fatal(pinned, known, err)
		}
		after, _ := os.ReadFile(path)
		if string(before) != string(after) {
			t.Fatal("pin operation mutated committed manifest")
		}
	}
}

func TestRepositoryLockRejectsFIFOWithoutBlocking(t *testing.T) {
	p := fixture(t)
	_, root, err := OpenRepository(p.Destinations[0], true)
	if err != nil {
		t.Fatal(err)
	}
	if err = unix.Mkfifo(filepath.Join(root, ".write.lock"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, exclusive := range []bool{false, true} {
		if unlock, err := LockRepository(p.Destinations[0], exclusive, false); err == nil {
			unlock()
			t.Fatal("special repository lock accepted")
		}
	}
}
