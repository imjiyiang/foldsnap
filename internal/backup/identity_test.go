package backup

import (
	"context"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHashDoesNotFollowLinksAndRejectsConcurrentMutation(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "file")
	os.WriteFile(path, []byte("before"), 0644)
	link := filepath.Join(root, "link")
	os.Symlink(path, link)
	var read int64
	ctx := Observe(context.Background(), func(e WorkEvent) { read += e.Bytes })
	if _, err := HashFile(ctx, link); err == nil || read != 0 {
		t.Fatal("hash followed symlink")
	}
	changed := false
	ctx = Observe(context.Background(), func(e WorkEvent) {
		if e.Path != "" && !changed {
			changed = true
			os.WriteFile(path, []byte("after!"), 0644)
		}
	})
	if _, err := HashFile(ctx, path); err == nil {
		t.Fatal("accepted a changing file version")
	}
}
func TestReplacedSourceRootCannotBecomeEmptyBackup(t *testing.T) {
	p := fixture(t)
	root := p.Sources[0].Path
	os.Remove(root)
	os.WriteFile(root, []byte("directory replaced by file"), 0644)
	if _, _, err := Scan(context.Background(), p); err == nil {
		t.Fatal("source root file treated as empty directory")
	}
}
func TestHashRejectsFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fifo")
	if err := unix.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := HashFile(context.Background(), path); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO accepted as regular file")
		}
	case <-time.After(time.Second):
		t.Fatal("opening FIFO blocked hash worker")
	}
}
