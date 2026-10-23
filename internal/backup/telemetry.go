package backup

import (
	"context"
	"regexp"
	"strconv"
	"strings"
)

// WorkEvent reports logical bytes processed, never physical device I/O.
type WorkEvent struct {
	CacheHit                         bool
	CacheBytes                       int64
	Phase, Path, Source, Destination string
	Bytes                            int64
	FileDone                         bool
	FilePercent                      *int
}
type observerKey struct{}

func Observe(ctx context.Context, fn func(WorkEvent)) context.Context {
	return context.WithValue(ctx, observerKey{}, fn)
}
func Report(ctx context.Context, e WorkEvent) {
	if fn, ok := ctx.Value(observerKey{}).(func(WorkEvent)); ok {
		fn(e)
	}
}

var progressLine = regexp.MustCompile(`^\s*([0-9,]+)\s+([0-9]+)%\s+`)

// rsync --progress uses CR updates and LF completions. Keep only bounded lines;
// filenames are deliberately not parsed as protocol records.
type progressWriter struct {
	ctx      context.Context
	pending  []byte
	previous int64
	complete bool
}

func (p *progressWriter) Write(b []byte) (int, error) {
	for _, c := range b {
		if c == '\r' || c == '\n' {
			p.line(string(p.pending))
			p.pending = p.pending[:0]
		} else if len(p.pending) < 8192 {
			p.pending = append(p.pending, c)
		}
	}
	return len(b), nil
}
func (p *progressWriter) line(s string) {
	m := progressLine.FindStringSubmatch(s)
	if m == nil {
		return
	}
	n, _ := strconv.ParseInt(strings.ReplaceAll(m[1], ",", ""), 10, 64)
	percent, _ := strconv.Atoi(m[2])
	if percent > 100 {
		return
	}
	if p.complete {
		p.previous = 0
		p.complete = false
	}
	delta := n - p.previous
	if delta < 0 {
		delta = n
	}
	p.previous = n
	done := percent == 100
	p.complete = done
	Report(p.ctx, WorkEvent{Bytes: delta, FileDone: done, FilePercent: &percent})
}
