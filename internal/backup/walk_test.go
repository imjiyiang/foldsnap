package backup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestDirectoryReadCancellationAndTimeout(t *testing.T) {
	for _, cancelled := range []bool{true, false} {
		t.Run(map[bool]string{true: "cancel", false: "timeout"}[cancelled], func(t *testing.T) {
			entered, release, exited := make(chan struct{}), make(chan struct{}), make(chan struct{})
			defer close(release)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				_, err := newDirectoryReader(4).read(ctx, "blocked", 50*time.Millisecond, func(string) ([]os.DirEntry, error) { close(entered); <-release; close(exited); return nil, nil })
				result <- err
			}()
			<-entered
			if cancelled {
				cancel()
			}
			select {
			case err := <-result:
				want := context.DeadlineExceeded
				if cancelled {
					want = context.Canceled
				}
				if !errors.Is(err, want) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("directory read prevented cancellation")
			}
			// The OS read is still outstanding, but the caller already terminated.
			select {
			case <-exited:
				t.Fatal("test read unexpectedly finished")
			default:
			}
		})
	}
}
func TestDirectoryReadWaitIsBounded(t *testing.T) {
	// A private set of blocked reads consumes all permits. A fifth request must
	// time out without spawning another OS call; release every simulated call.
	release := make(chan struct{})
	defer close(release)
	entered := make(chan struct{}, 4)
	reader := newDirectoryReader(4)
	for i := 0; i < 4; i++ {
		path := fmt.Sprintf("blocked-%d", i)
		go func() {
			_, _ = reader.read(context.Background(), path, time.Second, func(string) ([]os.DirEntry, error) { entered <- struct{}{}; <-release; return nil, nil })
		}()
	}
	for i := 0; i < 4; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("read did not start")
		}
	}
	called := false
	_, err := reader.read(context.Background(), "queued", 10*time.Millisecond, func(string) ([]os.DirEntry, error) { called = true; return nil, nil })
	if !errors.Is(err, context.DeadlineExceeded) || called {
		t.Fatal("unbounded read", err, called)
	}
}
func TestWalkSourceMatchesWalkDirWithoutFollowingLinks(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"a", "a/nested", "skip"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"a/one", "a/nested/two", "skip/hidden", "z"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "a"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	for _, skip := range []string{"skip", "a/one", "a/nested"} {
		var expected, actual []string
		visitor := func(out *[]string) fs.WalkDirFunc {
			return func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				rel, _ := filepath.Rel(root, path)
				*out = append(*out, rel)
				if rel == skip {
					return fs.SkipDir
				}
				return nil
			}
		}
		if err := filepath.WalkDir(root, visitor(&expected)); err != nil {
			t.Fatal(err)
		}
		if err := walkSource(context.Background(), root, visitor(&actual)); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(expected, actual) {
			t.Fatal(expected, actual)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := walkSource(ctx, root, func(string, fs.DirEntry, error) error { t.Fatal("visited after cancellation"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestCancelledDirectoryReadDoesNotStartOSCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := newDirectoryReader(4).read(ctx, "unused", time.Second, func(string) ([]os.DirEntry, error) { t.Error("started a read after cancellation"); return nil, nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// Retrying a timed-out directory must not consume the remaining permits or
// accept a late listing from the cancelled request as a fresh scan.
func TestDirectoryRetryDoesNotExhaustReadersOrReuseLateResults(t *testing.T) {
	reader := newDirectoryReader(2)
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	exited := make(chan struct{})
	_, err := reader.read(context.Background(), "blocked", 10*time.Millisecond, func(string) ([]os.DirEntry, error) {
		defer close(exited)
		<-release
		return nil, errors.New("late result")
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		_, err = reader.read(context.Background(), "blocked", time.Second, func(string) ([]os.DirEntry, error) { t.Error("duplicate OS read"); return nil, nil })
		if !errors.Is(err, errDirectoryReadPending) {
			t.Fatal(err)
		}
	}
	_, err = reader.read(context.Background(), "healthy", time.Second, func(string) ([]os.DirEntry, error) { return nil, nil })
	if err != nil {
		t.Fatal("blocked retries prevented a healthy directory read:", err)
	}
	close(release)
	<-exited
	// Wait for the reader to remove the completed call, not merely for the
	// injected OS function to return.
	reader.mu.Lock()
	pending := reader.pending["blocked"]
	reader.mu.Unlock()
	if pending != nil {
		<-pending.done
	}
	called := false
	_, err = reader.read(context.Background(), "blocked", time.Second, func(string) ([]os.DirEntry, error) { called = true; return nil, nil })
	if err != nil || !called {
		t.Fatal("did not perform fresh read after recovery", err)
	}
}
