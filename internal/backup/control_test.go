package backup

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestCooperativePauseResumeAndCancel(t *testing.T) {
	c := NewControl(nil)
	if err := c.Pause(); err != nil {
		t.Fatal(err)
	}
	if c.State() != "pausing" {
		t.Fatal(c.State())
	}
	ctx, cancel := context.WithCancel(WithControl(context.Background(), c))
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Checkpoint(ctx) }()
	deadline := time.Now().Add(time.Second)
	for c.State() != "paused" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if c.State() != "paused" {
		t.Fatal("gate did not acknowledge pause")
	}
	select {
	case <-done:
		t.Fatal("paused gate passed")
	case <-time.After(30 * time.Millisecond):
	}
	if err := c.Resume(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	c.Pause()
	go func() { done <- Checkpoint(ctx) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("paused gate ignored cancellation")
	}
}
func TestProcessGroupPauseResumeAndCancellation(t *testing.T) {
	output := filepath.Join(t.TempDir(), "ticks")
	c := NewControl(nil)
	ctx, cancel := context.WithCancel(WithControl(context.Background(), c))
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", `while :; do printf x >> "$1"; sleep 0.05; done`, "localbackup-test", output)
	done := make(chan error, 1)
	go func() { done <- RunCommand(ctx, cmd) }()
	size := func() int64 {
		st, _ := os.Stat(output)
		if st == nil {
			return 0
		}
		return st.Size()
	}
	deadline := time.Now().Add(2 * time.Second)
	for size() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if size() < 2 {
		t.Fatal("child did not start")
	}
	if err := c.Pause(); err != nil {
		t.Fatal(err)
	}
	if c.State() != "paused" {
		t.Fatal("child pause was not acknowledged")
	}
	time.Sleep(100 * time.Millisecond)
	before := size()
	time.Sleep(180 * time.Millisecond)
	if size() != before {
		t.Fatal("child group kept writing while paused")
	}
	c.Resume()
	deadline = time.Now().Add(time.Second)
	for size() == before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if size() == before {
		t.Fatal("child did not resume")
	}
	c.Pause()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled process reported success")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("paused process group did not cancel")
	}
	before = size()
	time.Sleep(150 * time.Millisecond)
	if size() != before {
		t.Fatal("descendant survived cancellation")
	}
}
