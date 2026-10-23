//go:build darwin && cgo

package fsevents

import (
	"sync"
	"time"

	native "github.com/fsnotify/fsevents"
)

func Available() bool { return true }
func Current() uint64 { return native.LatestEventID() }

type stream struct {
	once       sync.Once
	stop, done chan struct{}
}

func (s *stream) Stop() { s.once.Do(func() { close(s.stop) }); <-s.done }
func Start(paths []string, since uint64, emit func([]Event)) (Stream, error) {
	current := Current()
	if since == 0 || since > current {
		since = current
	}
	es := &native.EventStream{Paths: paths, Resume: true, EventID: since, Flags: native.FileEvents | native.WatchRoot | native.NoDefer, Latency: 500 * time.Millisecond, Events: make(chan []native.Event, 64)}
	if e := es.Start(); e != nil {
		return nil, e
	}
	s := &stream{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(s.done)
		stopSignal := s.stop
		var stopped chan struct{}
		for {
			select {
			case <-stopSignal:
				stopSignal = nil
				stopped = make(chan struct{})
				go func() { es.Stop(); close(stopped) }()
			case <-stopped:
				return
			case batch := <-es.Events:
				if stopped != nil {
					continue
				}
				events := make([]Event, 0, len(batch))
				for _, e := range batch {
					events = append(events, Event{Path: e.Path, ID: e.ID, Flags: uint32(e.Flags)})
				}
				emit(events)
			}
		}
	}()
	return s, nil
}
