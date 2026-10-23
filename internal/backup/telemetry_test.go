package backup

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestProgressCRChunksAndBoundedLines(t *testing.T) {
	var total int64
	var files int
	var percent int
	ctx := Observe(context.Background(), func(e WorkEvent) {
		total += e.Bytes
		if e.FileDone {
			files++
		}
		if e.FilePercent != nil {
			percent = *e.FilePercent
		}
	})
	w := &progressWriter{ctx: ctx}
	for _, s := range []string{"      1,0", "24 50% 1MB/s 00:00:01\r   2,048 100% 1MB/s 00:00:00 (xfer#1)\n", " 512 100% 1MB/s 00:00:00 (xfer#2)\n", strings.Repeat("x", 20000)} {
		w.Write([]byte(s))
	}
	if total != 2560 || files != 2 || percent != 100 || len(w.pending) > 8192 {
		t.Fatalf("bad stream: bytes=%d files=%d percent=%d buffer=%d", total, files, percent, len(w.pending))
	}
}
func TestRealRsyncReportsTransferAndVerification(t *testing.T) {
	rs := DetectRsync("")
	if !rs.Available {
		t.Skip("requires rsync")
	}
	p := fixture(t)
	write(t, filepath.Join(p.Sources[0].Path, "进度 file.txt"), strings.Repeat("data", 1024*256))
	var phase string
	totals := map[string]int64{}
	ctx := Observe(context.Background(), func(e WorkEvent) {
		if e.Phase != "" {
			phase = e.Phase
		}
		totals[phase] += e.Bytes
	})
	entries, _, err := Scan(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := Execute(ctx, p, p.Destinations[0], entries, nil, rs, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if totals["transfer"] != 1024*1024 || totals["verify"] != 1024*1024 {
		t.Fatalf("missing real telemetry: %+v", totals)
	}
	if err = Verify(context.Background(), snap.Path, entries); err != nil {
		t.Fatal(err)
	}
}
