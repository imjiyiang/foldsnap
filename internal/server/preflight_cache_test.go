package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"localbackup/internal/backup"
)

func countedRsync(t *testing.T, s *Server) (string, string, string) {
	t.Helper()
	real := backup.DetectRsync("")
	if !real.Available {
		t.Skip("requires rsync")
	}
	dir := t.TempDir()
	path, calls, fail := filepath.Join(dir, "rsync"), filepath.Join(dir, "calls"), filepath.Join(dir, "fail")
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = --dry-run ]; then
 echo preflight >> %q
 printf '%%s\n' "$@" >> %q
 while [ -e %q ]; do sleep 0.01; done
 if [ -e %q ]; then echo intentional-preflight-failure >&2; exit 12; fi
fi
exec %q "$@"
`, calls, calls+".args", filepath.Join(dir, "wait"), fail, real.Path)
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.settings.RsyncPath = path
	s.mu.Unlock()
	return path, calls, fail
}

func preflightCalls(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "preflight\n")
}

func TestPreviewChecksSourcesWithoutPassingThemToRsync(t *testing.T) {
	s := testServer(t)
	p := savedPreviewPlan(t, s)
	tool, calls, _ := countedRsync(t, s)
	first := getPreview(t, s, p)
	second := getPreview(t, s, p)
	if first.PreflightCacheHits != 0 || second.PreflightCacheHits != 1 || preflightCalls(t, calls) != 1 {
		t.Fatal("unchanged preview did not reuse a successful preflight")
	}
	if second.Added != first.Added || second.Files != first.Files || second.TransferBytes != first.TransferBytes {
		t.Fatal("cache changed preview semantics")
	}
	if first.PreviewID == second.PreviewID {
		t.Fatal("reused an approval instead of compatibility result")
	}
	if err := os.WriteFile(filepath.Join(p.Sources[0].Path, "one.txt"), []byte("changed source contents"), 0644); err != nil {
		t.Fatal(err)
	}
	if v := getPreview(t, s, p); v.PreflightCacheHits != 1 || preflightCalls(t, calls) != 1 || v.ScanStats.FilesHashed == 0 {
		t.Fatal("source change repeated the tool probe")
	}
	if v := getPreview(t, s, p); v.PreflightCacheHits != 1 {
		t.Fatal("changed manifest did not warm cache")
	}

	// Policy changes still affect the freshly scanned preview, without changing
	// the capabilities needed by the fixed tool probe.
	p.Rules = []backup.Rule{{Action: "exclude", Pattern: "one.txt"}}
	if err := s.Store.Put("backup_plans", p.ID, p); err != nil {
		t.Fatal(err)
	}
	v := getPreview(t, s, p)
	if v.Files != 1 || v.PreflightCacheHits != 1 || preflightCalls(t, calls) != 1 {
		t.Fatalf("policy scan bypassed: %+v", v)
	}
	args, err := os.ReadFile(calls + ".args")
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range p.Sources {
		if strings.Contains(string(args), src.Path) {
			t.Fatal("preview passed a user source to rsync")
		}
	}
	for _, dst := range p.Destinations {
		if strings.Contains(string(args), dst.Path) {
			t.Fatal("preview passed a user target to rsync")
		}
	}
	if !strings.Contains(string(args), "localbackup-rsync-capabilities-") {
		t.Fatal("missing generated probe namespace")
	}
	// Replace the tool at the same path with identical contents/version/mtime.
	info, _ := os.Stat(tool)
	data, _ := os.ReadFile(tool)
	replacement := tool + ".next"
	if err := os.WriteFile(replacement, data, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(replacement, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, tool); err != nil {
		t.Fatal(err)
	}
	if v := getPreview(t, s, p); v.PreflightCacheHits != 0 || preflightCalls(t, calls) != 2 {
		t.Fatal("replaced executable reused preflight")
	}
	s.preflightCache.mu.Lock()
	for key := range s.preflightCache.success {
		s.preflightCache.success[key] = time.Now().Add(-time.Second)
	}
	s.preflightCache.mu.Unlock()
	if v := getPreview(t, s, p); v.PreflightCacheHits != 0 || preflightCalls(t, calls) != 3 {
		t.Fatal("expired capability result did not repeat the small probe")
	}
}

func TestExecutionCannotReusePreviewPreflight(t *testing.T) {
	s := testServer(t)
	p := savedPreviewPlan(t, s)
	_, calls, fail := countedRsync(t, s)
	v := getPreview(t, s, p)
	if getPreview(t, s, p).PreflightCacheHits != 1 {
		t.Fatal("cache was not warm")
	}
	// Keep the executable and source unchanged, but make a real preflight fail.
	if err := os.WriteFile(fail, nil, 0600); err != nil {
		t.Fatal(err)
	}
	r := apiRun(t, s, p, RunRequest{PreviewID: v.PreviewID})
	if r.Status != "failed" || !strings.Contains(r.Error, "intentional-preflight-failure") || preflightCalls(t, calls) != 2 {
		t.Fatalf("execution trusted cache: %+v", r)
	}
	snaps, err := s.Store.Snapshots()
	if err != nil || len(snaps) != 0 {
		t.Fatal("failed full preflight published history", err)
	}
}

func TestFailedAndCancelledPreflightNeverCached(t *testing.T) {
	s := testServer(t)
	p := savedPreviewPlan(t, s)
	_, calls, fail := countedRsync(t, s)
	if err := os.WriteFile(fail, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := s.createPreview(context.Background(), p, nil); err == nil {
			t.Fatal("failed preflight accepted")
		}
	}
	if preflightCalls(t, calls) != 2 {
		t.Fatal("cached a failed check")
	}
	if err := os.Remove(fail); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.createPreview(ctx, p, nil); err == nil {
		t.Fatal("cancelled preview accepted")
	}
	if v := getPreview(t, s, p); v.PreflightCacheHits != 0 {
		t.Fatal("cancelled check warmed cache")
	}
}

func TestPreflightCacheExpiresAndIsBounded(t *testing.T) {
	var c preflightCache
	now := time.Now()
	c.remember("first", now)
	if !c.contains("first", now) || c.contains("first", now.Add(preflightCacheTTL)) {
		t.Fatal("incorrect cache lifetime")
	}
	for i := 0; i < preflightCacheLimit+5; i++ {
		c.remember(fmt.Sprint(i), now.Add(time.Duration(i)*time.Millisecond))
	}
	if len(c.success) > preflightCacheLimit {
		t.Fatal("unbounded cache")
	}
}

func TestCancelledRunningPreflightDoesNotWarmCache(t *testing.T) {
	s := testServer(t)
	p := savedPreviewPlan(t, s)
	tool, calls, _ := countedRsync(t, s)
	gate := filepath.Join(filepath.Dir(tool), "wait")
	if err := os.WriteFile(gate, nil, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := s.createPreview(ctx, p, nil); done <- err }()
	deadline := time.Now().Add(5 * time.Second)
	for preflightCalls(t, calls) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("preflight did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled preflight accepted")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("preflight cancellation blocked")
	}
	if err := os.Remove(gate); err != nil {
		t.Fatal(err)
	}
	if v := getPreview(t, s, p); v.PreflightCacheHits != 0 {
		t.Fatal("cancelled running check warmed cache")
	}
}
