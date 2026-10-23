package backup

import (
	"context"
	"path/filepath"
	"testing"
)

func TestUnchangedReadsEveryBaselineAndSourceFileExactlyOnce(t *testing.T) {
	p := fixture(t)
	rs := DetectRsync("")
	if !rs.Available {
		t.Skip("requires rsync")
	}
	write(t, filepath.Join(p.Sources[0].Path, "a.txt"), "AAA")
	write(t, filepath.Join(p.Sources[0].Path, "b.txt"), "BBBB")
	entries, _, err := Scan(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	first, err := Execute(context.Background(), p, p.Destinations[0], entries, nil, rs, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	bytes := map[string]int64{}
	files := map[string]int{}
	phase := ""
	ctx := Observe(context.Background(), func(e WorkEvent) {
		if e.Phase != "" {
			phase = e.Phase
		}
		bytes[phase] += e.Bytes
		if e.Path != "" && !e.CacheHit {
			files[phase]++
		}
	})
	next, err := Execute(ctx, p, p.Destinations[0], entries, first, rs, func(string) {})
	if err != nil || next != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"verify_baseline", "verify_source"} {
		if bytes[phase] != 7 || files[phase] != 2 {
			t.Fatal("content read count changed", phase, bytes, files)
		}
	}
	// APFS closes with just-proven hashes; other filesystems conservatively reread.
	if _, cacheable := cacheFilesystem(p.Sources[0].Path); cacheable && bytes["verify_closing"] != 0 {
		// Scan() above has no persisted volume identities, so it cannot seed APFS
		// closing fingerprints. Verify the production indexed variant separately.
		for i := range entries {
			entries[i].VolumeID = p.Sources[0].VolumeID
		}
		bytes = map[string]int64{}
		files = map[string]int{}
		next, err = Execute(ctx, p, p.Destinations[0], entries, first, rs, func(string) {})
		if err != nil || next != nil || bytes["verify_closing"] != 0 {
			t.Fatal("closing scan reread verified APFS contents", bytes, err)
		}
	}
}
