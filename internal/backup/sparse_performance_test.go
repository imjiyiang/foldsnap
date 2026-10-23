package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Opt-in real-rsync workload. Every source/repository is owned by t.TempDir;
// neither installed app state nor personal directories are opened.
func TestSparseChangePerformanceWorkload(t *testing.T) {
	reportDir := os.Getenv("LOCALBACKUP_SPARSE_REPORT")
	if reportDir == "" {
		t.Skip("opt-in independent workload")
	}
	n := 10000
	if value := os.Getenv("LOCALBACKUP_SPARSE_FILES"); value != "" {
		var err error
		n, err = strconv.Atoi(value)
		if err != nil || n < 100 {
			t.Fatal("invalid file count")
		}
	}
	if err := os.MkdirAll(reportDir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"snapshot", "incremental", "mirror"} {
		t.Run(mode, func(t *testing.T) {
			p := fixture(t)
			p.Mode = mode
			p.Sources = p.Sources[:1]
			rs := DetectRsync("")
			if !rs.Available {
				t.Skip("requires rsync")
			}
			src := p.Sources[0].Path
			for i := 0; i < n; i++ {
				write(t, filepath.Join(src, fmt.Sprintf("d%03d/f%06d.bin", i/100, i)), strings.Repeat(string(rune('a'+i%26)), 8192))
			}
			for i := 0; i < 3; i++ {
				write(t, filepath.Join(src, fmt.Sprintf("large%d.bin", i)), strings.Repeat("L", 8*1024*1024))
			}
			entries, _, err := Scan(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			first, err := Execute(context.Background(), p, p.Destinations[0], entries, nil, rs, func(string) {})
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 5; i++ {
				write(t, filepath.Join(src, fmt.Sprintf("d000/f%06d.bin", i)), strings.Repeat("Z", 8192))
			}
			for i := 5; i < 10; i++ {
				if err = os.Rename(filepath.Join(src, fmt.Sprintf("d000/f%06d.bin", i)), filepath.Join(src, fmt.Sprintf("d000/moved%06d.bin", i))); err != nil {
					t.Fatal(err)
				}
			}
			for i := 10; i < 13; i++ {
				if err = os.Remove(filepath.Join(src, fmt.Sprintf("d000/f%06d.bin", i))); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < 3; i++ {
				write(t, filepath.Join(src, fmt.Sprintf("new%d.bin", i)), strings.Repeat("N", 8192))
			}
			entries, _, err = Scan(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			p.MirrorDeletesAuthorized = true
			counts := map[string]int{}
			readBytes := map[string]int64{}
			lastPath := ""
			logs := []string{}
			ctx := Observe(context.Background(), func(e WorkEvent) {
				if e.Path != "" && !e.CacheHit {
					lastPath = e.Path
					counts[e.Path]++
				}
				if e.Bytes > 0 {
					readBytes[lastPath] += e.Bytes
				}
			})
			started := time.Now()
			next, err := Execute(ctx, p, p.Destinations[0], entries, first, rs, func(s string) { logs = append(logs, s) })
			elapsed := time.Since(started).Seconds()
			if err != nil || next == nil {
				t.Fatal(err)
			}
			group := func(path string) string {
				if Contains(src, path) {
					return "source"
				}
				if Contains(first.Path, path) {
					return "baseline"
				}
				return "staging"
			}
			reads := map[string]int{}
			bytes := map[string]int64{}
			for path, count := range counts {
				reads[group(path)] += count
				size := int64(8192)
				if strings.HasPrefix(filepath.Base(path), "large") {
					size = 8 * 1024 * 1024
				}
				bytes[group(path)] += int64(count) * size
			}
			// Post-timing audits prove both generations are independently valid.
			if err = AuditSnapshot(context.Background(), *first); err != nil {
				t.Fatal("history changed", err)
			}
			if err = AuditSnapshot(context.Background(), *next); err != nil {
				t.Fatal("new generation invalid", err)
			}
			a, _ := os.Stat(filepath.Join(first.Path, p.Sources[0].ID, "large0.bin"))
			b, _ := os.Stat(filepath.Join(next.Path, p.Sources[0].ID, "large0.bin"))
			if !os.SameFile(a, b) {
				t.Fatal("unchanged large file retransferred")
			}
			restored, err := Restore(context.Background(), *next, RestoreRequest{SourceID: p.Sources[0].ID, Destination: t.TempDir()}, []Plan{p}, rs)
			if err != nil {
				t.Fatal("whole generation restore failed", err)
			}
			data, err := os.ReadFile(filepath.Join(restored, "d000/f000000.bin"))
			if err != nil || string(data) != strings.Repeat("Z", 8192) {
				t.Fatal("restored modification differs", err)
			}
			_, err = os.Stat(filepath.Join(restored, "d000/f000010.bin"))
			if mode == "incremental" && err != nil {
				t.Fatal("incremental retained file lost", err)
			}
			if mode != "incremental" && !os.IsNotExist(err) {
				t.Fatal("removed file unexpectedly restored", err)
			}
			oldRestore, err := Restore(context.Background(), *first, RestoreRequest{SourceID: p.Sources[0].ID, Path: "d000/f000000.bin", Destination: t.TempDir()}, []Plan{p}, rs)
			if err != nil {
				t.Fatal(err)
			}
			data, err = os.ReadFile(filepath.Join(oldRestore, "f000000.bin"))
			if err != nil || string(data) != strings.Repeat("a", 8192) {
				t.Fatal("historical version restore differs", err)
			}
			result := map[string]any{"version": Version, "mode": mode, "smallFiles": n, "smallFileBytes": 8192, "largeFiles": 3, "largeFileBytes": 8 * 1024 * 1024, "modified": 5, "moved": 5, "removed": 3, "added": 3, "executeSeconds": elapsed, "goHashReads": reads, "goHashBytes": bytes, "byteAccounting": "successful full-file hash starts times known deterministic fixture sizes; rsync progress bytes excluded", "sampleUnchangedLargeBaselineReads": counts[filepath.Join(first.Path, p.Sources[0].ID, "large0.bin")], "sampleUnchangedLargeSourceReads": counts[filepath.Join(src, "large0.bin")], "logs": logs, "historyAuditPassed": true, "newGenerationAuditPassed": true, "independentTemporaryDataOnly": true, "fullRestorePassed": true, "historicalFileRestorePassed": true, "scope": "Go SHA256 content reads during Execute only; excludes rsync internal reads, prior scan, post-timing audits, metadata and fsync. Timings include full preflight, linking, final verification and durable publication; OS caches not flushed."}
			data, _ = json.MarshalIndent(result, "", "  ")
			if err = os.WriteFile(filepath.Join(reportDir, mode+".json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			t.Log(string(data))
		})
	}
}
