//go:build darwin && cgo

package fsevents

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func waitForPath(t *testing.T, events <-chan []Event, path string, after uint64) uint64 {
	t.Helper()
	deadline := time.NewTimer(12 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case batch := <-events:
			for _, e := range batch {
				if e.Path == path && e.ID > after && e.Flags&HistoryDone == 0 {
					return e.ID
				}
			}
		case <-deadline.C:
			t.Fatalf("no native event for %s after %d", path, after)
			return 0
		}
	}
}
func TestNativeChangeAndJournalReplay(t *testing.T) {
	root, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(root, "watched.txt")
	events := make(chan []Event, 128)
	cursor := Current()
	stream, e := Start([]string{root}, cursor, func(v []Event) { events <- v })
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if stream != nil {
			stream.Stop()
		}
	}()
	if e = os.WriteFile(file, []byte("first"), 0644); e != nil {
		t.Fatal(e)
	}
	first := waitForPath(t, events, file, cursor)
	stream.Stop()
	stream = nil
	for len(events) > 0 {
		<-events
	}
	if e = os.WriteFile(file, []byte("changed while stopped"), 0644); e != nil {
		t.Fatal(e)
	}
	stream, e = Start([]string{root}, first, func(v []Event) { events <- v })
	if e != nil {
		t.Fatal(e)
	}
	waitForPath(t, events, file, first)
}
