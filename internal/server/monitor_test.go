package server

import (
	"context"
	"localbackup/internal/backup"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDiskRateResetsAndPlist(t *testing.T) {
	r, w := rates(diskCounter{Read: 100, Write: 200}, diskCounter{Read: 300, Write: 800}, 2)
	if *r != 100 || *w != 300 {
		t.Fatal("incorrect bytes/s")
	}
	r, w = rates(diskCounter{Read: 100}, diskCounter{Read: 1}, 1)
	if r != nil || w != nil {
		t.Fatal("reset must be unavailable")
	}
	v, err := decodePlist(strings.NewReader(`<plist><dict><key>Internal</key><false/><key>Statistics</key><dict><key>Bytes (Read)</key><integer>4294967296</integer></dict><key>Media</key><array><string>磁盘</string></array></dict></plist>`))
	if err != nil {
		t.Fatal(err)
	}
	d := v.(map[string]any)
	if d["Internal"] != false || d["Statistics"].(map[string]any)["Bytes (Read)"] != uint64(4294967296) {
		t.Fatal(d)
	}
}
func TestMonitorStagesAndAuthenticatedAPI(t *testing.T) {
	s := testServer(t)
	observe := s.observeRun("run-test")
	observe(backup.WorkEvent{Bytes: 1024, Path: "/source/file"})
	s.sampleMonitor(context.Background())
	observe(backup.WorkEvent{Phase: "transfer", Bytes: 2048})
	s.monitor.mu.Lock()
	if s.monitor.work.Bytes != 2048 || s.monitor.work.Path != "" {
		t.Fatal("phase totals mixed")
	}
	s.monitor.mu.Unlock()
	response := request(t, s, "GET", "system/monitor", nil)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "run-test") {
		t.Fatal(response.Body.String())
	}
	observe(backup.WorkEvent{Phase: "done"})
	s.sampleMonitor(context.Background())
	if s.monitor.work.BytesPerSecond != 0 {
		t.Fatal("finished run has live rate")
	}
}
func TestNativeDiskSampling(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS device metrics")
	}
	m := monitorState{}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, err := readDiskCounters(ctx, &m)
	if err != nil {
		t.Fatal(err)
	}
	if len(c) == 0 {
		t.Skip("driver exposes no physical devices")
	}
	for _, d := range c {
		if d.Device == "" || d.ID == "" {
			t.Fatal(d)
		}
	}
}
