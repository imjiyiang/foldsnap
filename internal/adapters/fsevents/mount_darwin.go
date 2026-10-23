package fsevents

import (
	"context"

	"golang.org/x/sys/unix"
)

// WatchMounts observes only the /Volumes directory vnode, never recursively
// subscribing to every file on every mounted backup disk.
func WatchMounts(ctx context.Context, notify func()) error {
	fd, e := unix.Open("/Volumes", unix.O_EVTONLY|unix.O_CLOEXEC, 0)
	if e != nil {
		return e
	}
	defer unix.Close(fd)
	queue, e := unix.Kqueue()
	if e != nil {
		return e
	}
	defer unix.Close(queue)
	changes := []unix.Kevent_t{{Ident: uint64(fd), Filter: unix.EVFILT_VNODE, Flags: unix.EV_ADD | unix.EV_CLEAR, Fflags: unix.NOTE_WRITE | unix.NOTE_EXTEND | unix.NOTE_DELETE | unix.NOTE_RENAME}}
	events := make([]unix.Kevent_t, 4)
	for {
		if e = ctx.Err(); e != nil {
			return nil
		}
		timeout := unix.Timespec{Sec: 1}
		n, err := unix.Kevent(queue, changes, events, &timeout)
		changes = nil
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		if n > 0 {
			notify()
		}
	}
}
