package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"localbackup/internal/backup"
)

func TestUnchangedRunAuditableWithoutDuplicateSnapshot(t *testing.T) {
	for _, mode := range []string{"snapshot", "incremental", "mirror"} {
		t.Run(mode, func(t *testing.T) {
			s := testServer(t)
			p := saveFixturePlan(t, s, mode)
			initial := apiRun(t, s, p, RunRequest{})
			if initial.Status != "success" || initial.NoChanges || len(initial.CreatedSnapshotIDs) != 1 {
				t.Fatal(initial)
			}
			before, err := s.Store.Snapshots()
			if err != nil {
				t.Fatal(err)
			}
			preview := getPreview(t, s, p)
			next := apiRun(t, s, p, RunRequest{PreviewID: preview.PreviewID})
			if next.Status != "success" || !next.NoChanges || len(next.CreatedSnapshotIDs) != 0 || len(next.UnchangedDestinationIDs) != 1 || !strings.Contains(next.Stage, "未新建恢复点") {
				t.Fatal(next)
			}
			if !strings.Contains(strings.Join(next.Logs, "\n"), "文件目录索引更新完成：新增 0，更新 0，删除 0") {
				t.Fatal("unchanged run rewrote catalog", next.Logs)
			}
			after, err := s.Store.Snapshots()
			if err != nil || len(after) != 1 || after[0].ID != before[0].ID || !after[0].CreatedAt.Equal(before[0].CreatedAt) {
				t.Fatal("duplicate snapshot or changed history", err)
			}
			// Completion is persisted and every approval is still single-use.
			var saved backup.Run
			if err = s.Store.Get("backup_runs", next.ID, &saved); err != nil || !saved.NoChanges {
				t.Fatal(err)
			}
			w := request(t, s, "POST", "plans/"+p.ID+"/runs", RunRequest{PreviewID: preview.PreviewID})
			if w.Code != 409 {
				t.Fatal("unchanged run did not consume preview", w.Body.String())
			}
			if err = os.WriteFile(filepath.Join(p.Sources[0].Path, "new.txt"), []byte("changed selection"), 0644); err != nil {
				t.Fatal(err)
			}
			changed := apiRun(t, s, p, RunRequest{})
			if changed.Status != "success" || changed.NoChanges || len(changed.CreatedSnapshotIDs) != 1 {
				t.Fatal(changed)
			}
			after, err = s.Store.Snapshots()
			if err != nil || len(after) != 2 {
				t.Fatal("changed data did not create recovery point", err)
			}
		})
	}
}

func TestUnchangedDestinationDoesNotSkipNewDestination(t *testing.T) {
	s := testServer(t)
	p := planFixture(t)
	p.Destinations = append(p.Destinations, backup.Destination{Name: "New target", Path: t.TempDir(), Enabled: true})
	w := request(t, s, "POST", "plans", p)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	entries, _, err := backup.Scan(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	first, err := backup.Execute(context.Background(), p, p.Destinations[0], entries, nil, backup.DetectRsync(""), func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Store.Put("snapshots", first.ID, first); err != nil {
		t.Fatal(err)
	}
	run := apiRun(t, s, p, RunRequest{})
	if run.Status != "success" || run.NoChanges || len(run.UnchangedDestinationIDs) != 1 || run.UnchangedDestinationIDs[0] != p.Destinations[0].ID || len(run.CreatedSnapshotIDs) != 1 {
		t.Fatal(run)
	}
	snapshots, err := s.Store.Snapshots()
	if err != nil || len(snapshots) != 2 {
		t.Fatal("per-destination optimization lost new target", err)
	}
}
