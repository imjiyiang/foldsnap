package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"localbackup/internal/backup"
)

func TestDirectoryCreationBoundToBrowsedParent(t *testing.T) {
	s := testServer(t)
	parent := t.TempDir()
	w := request(t, s, "GET", "filesystem/browse?path="+parent, nil)
	var browse struct {
		Identity backup.Source `json:"identity"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &browse) != nil || !backup.SourceBound(browse.Identity) {
		t.Fatal(w.Body.String())
	}
	w = request(t, s, "POST", "filesystem/directories", map[string]any{"parent": browse.Identity, "name": "新的备份目录"})
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	if st, err := os.Stat(filepath.Join(parent, "新的备份目录")); err != nil || !st.IsDir() {
		t.Fatal("directory missing", err)
	}
	var result struct {
		AuditID string `json:"auditId"`
	}
	json.Unmarshal(w.Body.Bytes(), &result)
	var audit map[string]any
	if err := s.Store.Get("restore_jobs", result.AuditID, &audit); err != nil || audit["status"] != "success" {
		t.Fatal("missing audit", err)
	}
	if w = request(t, s, "POST", "filesystem/directories", map[string]any{"parent": browse.Identity, "name": "新的备份目录"}); w.Code != 400 {
		t.Fatal("existing path overwritten")
	}
	for _, name := range []string{"..", "../escape", "a/b", "a\\b", ""} {
		if w = request(t, s, "POST", "filesystem/directories", map[string]any{"parent": browse.Identity, "name": name}); w.Code != 400 {
			t.Fatal("unsafe name", name, w.Body.String())
		}
	}
	browse.Identity.DirectoryID = "wrong-directory"
	if w = request(t, s, "POST", "filesystem/directories", map[string]any{"parent": browse.Identity, "name": "wrong"}); w.Code != 409 {
		t.Fatal("wrong identity accepted", w.Body.String())
	}
}
func TestDirectoryCreationRejectsProtectedAndReplacedParents(t *testing.T) {
	s := testServer(t)
	p := savedPreviewPlan(t, s)
	for _, parent := range []string{p.Sources[0].Path, filepath.Dir(s.Store.Path)} {
		bound, err := backup.BindSource(backup.Source{Path: parent})
		if err != nil {
			t.Fatal(err)
		}
		w := request(t, s, "POST", "filesystem/directories", map[string]any{"parent": bound, "name": "forbidden"})
		if w.Code != 400 {
			t.Fatal("protected parent accepted", w.Body.String())
		}
	}
	parent := filepath.Join(t.TempDir(), "parent")
	os.Mkdir(parent, 0755)
	bound, err := backup.BindSource(backup.Source{Path: parent})
	if err != nil {
		t.Fatal(err)
	}
	os.Rename(parent, parent+"-old")
	os.Mkdir(parent, 0755)
	w := request(t, s, "POST", "filesystem/directories", map[string]any{"parent": bound, "name": "forbidden"})
	if w.Code != 409 {
		t.Fatal("replaced parent accepted", w.Body.String())
	}
	os.Remove(parent)
	os.Symlink(parent+"-old", parent)
	w = request(t, s, "POST", "filesystem/directories", map[string]any{"parent": bound, "name": "forbidden"})
	if w.Code != 400 {
		t.Fatal("linked parent accepted", w.Body.String())
	}
}
