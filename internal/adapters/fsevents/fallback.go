//go:build !darwin || !cgo

package fsevents

import "errors"

func Available() bool { return false }
func Current() uint64 { return 0 }
func Start(paths []string, since uint64, emit func([]Event)) (Stream, error) {
	return nil, errors.New("native FSEvents requires macOS with cgo enabled")
}
