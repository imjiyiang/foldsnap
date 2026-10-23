package server

import (
	"context"
	"localbackup/internal/backup"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadinessDoesNotExecuteConfiguredBinary(t *testing.T) {
	s := testServer(t)
	dir := t.TempDir()
	marker := filepath.Join(dir, "executed")
	binary := filepath.Join(dir, "rsync")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\ntouch '"+marker+"'\nsleep 3\n"), 0700); err != nil {
		t.Fatal(err)
	}
	s.settings.RsyncPath = binary
	start := time.Now()
	for i := 0; i < 3; i++ {
		w := request(t, s, "GET", "system/status", nil)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	if time.Since(start) > time.Second {
		t.Fatal("readiness blocked by subprocess")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("readiness executed diagnostic command")
	}
}

func TestNewDefersRepositoryAccessUntilBackgroundStart(t *testing.T) {
	s := testServer(t)
	p := planFixture(t)
	p.ID = "deferred-recovery"
	p.Enabled = false
	if err := backup.Validate(&p); err != nil {
		t.Fatal(err)
	}
	s.Store.Put("backup_plans", p.ID, p)
	entries, _, err := backup.Scan(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	sn, err := backup.Execute(context.Background(), p, p.Destinations[0], entries, nil, backup.DetectRsync(""), func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	restarted := New(s.Store, "test-token", "127.0.0.1:8787", "", false)
	all, _ := s.Store.Snapshots()
	if len(all) != 0 {
		t.Fatal("constructor accessed repository before HTTP startup")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	restarted.Start(ctx)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		all, _ = s.Store.Snapshots()
		if len(all) == 1 && all[0].ID == sn.ID {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("background recovery did not import committed snapshot")
}

func TestVolumeDiscoveryDoesNotBlockConcurrentPages(t *testing.T) {
	s := testServer(t)
	// Model an in-flight system disk query; every page must get the last result.
	s.volumeBusy = true
	s.volumeCache = []map[string]any{{"id": "cached-volume", "name": "Backup", "connected": true}}
	start := time.Now()
	for i := 0; i < 20; i++ {
		w := request(t, s, "GET", "volumes", nil)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "cached-volume") {
			t.Fatal(w.Body.String())
		}
	}
	if time.Since(start) > time.Second {
		t.Fatal("UI requests waited for disk query")
	}
	if _, ok := s.volumeCache[0]["bindings"]; ok {
		t.Fatal("response mutated shared cache")
	}
}

func TestCleanIndexedStartupDoesNotOpenRepository(t *testing.T) {
	s := testServer(t)
	p := backup.Plan{ID: "clean", Destinations: []backup.Destination{{ID: "disk", Path: "/not-mounted", VolumeID: "saved-id"}}}
	s.Store.Put("backup_plans", p.ID, p)
	s.Store.Put("snapshots", "snap", backup.Snapshot{ID: "snap", PlanID: p.ID, DestinationID: "disk", VolumeID: "saved-id"})
	start := time.Now()
	s.reconcile(context.Background())
	if time.Since(start) > time.Second {
		t.Fatal("clean startup accessed unavailable disk")
	}
}
