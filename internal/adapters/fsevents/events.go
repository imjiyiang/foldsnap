// Package fsevents isolates the native event stream from durable scheduling.
package fsevents

// Native flags are stable values defined by Apple's FSEvents API.
const (
	MustScanSubDirs uint32 = 0x1
	UserDropped     uint32 = 0x2
	KernelDropped   uint32 = 0x4
	EventIDsWrapped uint32 = 0x8
	HistoryDone     uint32 = 0x10
	RootChanged     uint32 = 0x20
	Mount           uint32 = 0x40
	Unmount         uint32 = 0x80
)

type Event struct {
	Path  string
	ID    uint64
	Flags uint32
}

func (e Event) NeedsRescan() bool {
	return e.Flags&(MustScanSubDirs|UserDropped|KernelDropped|EventIDsWrapped|RootChanged|Mount|Unmount) != 0
}
func (e Event) GlobalRescan() bool { return e.Flags&(UserDropped|KernelDropped|EventIDsWrapped) != 0 }

type Stream interface{ Stop() }
