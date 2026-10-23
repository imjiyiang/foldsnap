package backup

import (
	"context"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

func TestCanonicalMetadataOnlyDoesNotReadFIFO(t *testing.T) {
	root, err := Canonical(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(root, "pipe")
	if err = unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		got, e := Canonical(fifo)
		if e == nil && got != fifo {
			e = os.ErrInvalid
		}
		result <- e
	}()
	select {
	case err = <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("metadata path lookup attempted blocking read-open")
	}
	link := filepath.Join(root, "alias")
	if err = os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if got, e := Canonical(link); e != nil || got != root {
		t.Fatalf("symlink canonicalization: %q %v", got, e)
	}
}

func TestNativeVolumeUUIDMatchesDiskutil(t *testing.T) {
	paths := []string{"/", "/System/Volumes/Data"}
	if p := os.Getenv("LOCALBACKUP_TEST_VOLUME"); p != "" {
		paths = append(paths, p)
	}
	for _, path := range paths {
		got, err := VolumeID(path)
		if err != nil {
			t.Fatal(path, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		output, err := exec.CommandContext(ctx, "/usr/sbin/diskutil", "info", "-plist", path).Output()
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		expected := regexp.MustCompile(`<key>VolumeUUID</key>\s*<string>([^<]+)</string>`).FindSubmatch(output)
		if len(expected) != 2 || got != string(expected[1]) {
			t.Fatalf("UUID identity differs for %s", path)
		}
	}
}
