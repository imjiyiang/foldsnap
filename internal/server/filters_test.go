package server

import (
	"context"
	"encoding/json"
	"localbackup/internal/backup"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileFilterAPIPreviewAndMirrorProtection(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "mirror")
	first := apiRun(t, s, p, RunRequest{})
	if first.Status != "success" {
		t.Fatal(first.Error)
	}
	max := int64(20)
	p.FileFilters = backup.FileFilters{MaxBytes: &max, Types: []string{"document"}}
	response := request(t, s, "PATCH", "plans/"+p.ID, p)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	// Live file grows outside the scope; its old version must be retained.
	if err := os.WriteFile(filepath.Join(p.Sources[0].Path, "one.txt"), make([]byte, 100), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(p.Sources[1].Path, "two.txt")); err != nil {
		t.Fatal(err)
	}
	v := getPreview(t, s, p)
	if v.Retained != 1 || v.DeletePaths != 1 || v.Removals[0].Path != "two.txt" {
		t.Fatalf("dynamic scope became a deletion: %+v", v)
	}
	run := apiRun(t, s, p, RunRequest{PreviewID: v.PreviewID, ConfirmDeletes: true})
	if run.Status != "success" {
		t.Fatal(run.Error)
	}
	current, err := backup.ReadCurrent(p.Destinations[0], p.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(current.Path, p.Sources[0].ID, "one.txt"))
	if err != nil || string(b) != "first source" {
		t.Fatal("filtered old file lost", err)
	}
	// Complete rule preview explains included and ignored entries using the same policy.
	response = request(t, s, "POST", "plans/"+p.ID+"/rules/preview", p)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	var preview struct {
		Entries  []backup.Entry     `json:"entries"`
		Ignored  []backup.Operation `json:"ignored"`
		Selected []backup.Operation `json:"selectedReasons"`
	}
	if err = json.Unmarshal(response.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if len(preview.Entries) != 0 || len(preview.Ignored) != 1 || preview.Ignored[0].Reason == "" {
		t.Fatalf("bad rule preview: %+v", preview)
	}
	p.FileFilters.Exceptions = []backup.FileException{{Path: "one.txt", Action: "include"}}
	response = request(t, s, "POST", "plans/"+p.ID+"/rules/preview", p)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	if err = json.Unmarshal(response.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if len(preview.Entries) != 1 || len(preview.Selected) != 1 || !strings.Contains(preview.Selected[0].Reason, "单文件例外") {
		t.Fatalf("included file has no decision reason: %+v", preview)
	}
	p.FileFilters.Types = []string{"unrecognized"}
	if response = request(t, s, "PATCH", "plans/"+p.ID, p); response.Code != 400 {
		t.Fatal("invalid policy accepted", response.Body.String())
	}

}
func TestFileFilterChangeDetectionAndPersistence(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	limit := int64(4)
	p.FileFilters.MaxBytes = &limit
	p.FileFilters.Exceptions = []backup.FileException{{Path: ".cache/required.txt", Action: "include"}}
	p.Sources[0].ExcludeHidden = true
	writePath := filepath.Join(p.Sources[0].Path, ".cache/required.txt")
	os.MkdirAll(filepath.Dir(writePath), 0755)
	os.WriteFile(writePath, []byte("include"), 0644)
	response := request(t, s, "PATCH", "plans/"+p.ID, p)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	var persisted backup.Plan
	if err := s.Store.Get("backup_plans", p.ID, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.FileFilters.MaxBytes == nil || *persisted.FileFilters.MaxBytes != 4 {
		t.Fatal("filter policy not persisted")
	}
	if !selectedEvent(p, p.Sources[0], writePath) || !selectedEvent(p, p.Sources[0], filepath.Join(p.Sources[0].Path, "one.txt")) {
		t.Fatal("events crossing file-condition boundary were lost")
	}
	a, err := signature(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(p.Sources[0].Path, "one.txt"), []byte("yes"), 0644)
	b, err := signature(context.Background(), p)
	if err != nil || a == b {
		t.Fatal("signature ignored file entering filter scope", err)
	}
	now := time.Now()
	os.Chtimes(writePath, now, now)
	entries, _, err := backup.Scan(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	selected := map[string]bool{}
	for _, e := range entries {
		selected[e.Path] = true
	}
	if !selected["one.txt"] || !selected[".cache/required.txt"] {
		t.Fatal("scanner disagrees with change detection", selected)
	}
}
