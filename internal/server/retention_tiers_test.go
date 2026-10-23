package server

import (
	"encoding/json"
	"localbackup/internal/backup"
	"os"
	"path/filepath"
	"testing"
)

func TestRetentionRechecksAddedTierBeforeApply(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	for i := 0; i < 2; i++ {
		if err := os.WriteFile(filepath.Join(p.Sources[0].Path, "retention-version.txt"), []byte{byte('0' + i)}, 0644); err != nil {
			t.Fatal(err)
		}
		if r := apiRun(t, s, p, RunRequest{}); r.Status != "success" {
			t.Fatal(r.Error)
		}
	}
	w := request(t, s, "POST", "retention/preview", map[string]any{"planId": p.ID})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var candidates []backup.Snapshot
	if err := json.Unmarshal(w.Body.Bytes(), &candidates); err != nil || len(candidates) != 1 {
		t.Fatal(w.Body.String())
	}
	// A tier must protect the older snapshot once it represents an older month.
	old := candidates[0]
	if err := s.Store.Get("snapshots", old.ID, &old); err != nil {
		t.Fatal(err)
	}
	old.CreatedAt = old.CreatedAt.AddDate(0, -1, 0)
	if err := s.Store.Put("snapshots", old.ID, old); err != nil {
		t.Fatal(err)
	}
	p.Retention.KeepMonthly = 2
	w = request(t, s, "PATCH", "plans/"+p.ID, p)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = request(t, s, "POST", "retention/apply", map[string]any{"planId": p.ID, "ids": []string{old.ID}, "confirm": true})
	if w.Code != 409 {
		t.Fatalf("stale deletion accepted: %d %s", w.Code, w.Body.String())
	}
	w = request(t, s, "POST", "retention/preview", map[string]any{"planId": p.ID})
	if w.Code != 200 || w.Body.String() != "[]\n" {
		t.Fatal(w.Body.String())
	}
	if err := s.Store.Get("snapshots", old.ID, &old); err != nil {
		t.Fatal("protected snapshot removed", err)
	}
}
