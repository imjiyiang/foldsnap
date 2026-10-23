package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"localbackup/internal/backup"
)

func savedPreviewPlan(t *testing.T, s *Server) backup.Plan {
	t.Helper()
	p := planFixture(t)
	p.Enabled = false
	w := request(t, s, "POST", "plans", p)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	return p
}
func waitPreview(t *testing.T, s *Server, id string) previewJob {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		w := request(t, s, "GET", "preview-jobs/"+id, nil)
		var job previewJob
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &job) != nil {
			t.Fatal(w.Body.String())
		}
		if job.FinishedAt != nil {
			s.previews.mu.Lock()
			live := s.previews.live[id]
			s.previews.mu.Unlock()
			if live != nil {
				<-live.done
			}
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("preview timed out")
	return previewJob{}
}
func TestBackgroundPreviewSurvivesRequestAndDoesNotStartBackup(t *testing.T) {
	s := testServer(t)
	p := savedPreviewPlan(t, s)
	s.previews.slot <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest("POST", "http://127.0.0.1:8787/api/v1/plans/"+p.ID+"/preview-jobs", strings.NewReader(`{}`)).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	started := time.Now()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 202 || time.Since(started) > time.Second {
		t.Fatalf("blocking enqueue: %d %s", w.Code, w.Body)
	}
	var job previewJob
	json.Unmarshal(w.Body.Bytes(), &job)
	cancel() // Closing the initiating modal/tab must not stop background work.
	duplicate := request(t, s, "POST", "plans/"+p.ID+"/preview-jobs", map[string]any{})
	var same previewJob
	json.Unmarshal(duplicate.Body.Bytes(), &same)
	if same.ID != job.ID {
		t.Fatal("active duplicate not coalesced")
	}
	if request(t, s, "GET", "plans", nil).Code != 200 {
		t.Fatal("unresponsive API")
	}
	<-s.previews.slot
	finished := waitPreview(t, s, job.ID)
	if finished.State != "ready" || finished.Result == nil || finished.Result.Added != 2 {
		t.Fatalf("job: %+v", finished)
	}
	if finished.PathsRead == 0 || finished.BytesRead == 0 {
		t.Fatal("missing scan progress")
	}
	list := request(t, s, "GET", "preview-jobs", nil)
	if strings.Contains(list.Body.String(), `"result"`) || strings.Contains(list.Body.String(), `"operations"`) {
		t.Fatal("polling leaked large result")
	}
	var listed []previewJob
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil || len(listed) == 0 || listed[0].PreviewID != finished.Result.PreviewID {
		t.Fatal("polling summary lost preview identity")
	}
	var runs int
	s.Store.DB.QueryRow("SELECT count(*) FROM backup_runs").Scan(&runs)
	if runs != 0 {
		t.Fatal("preview started backup")
	}
	w = request(t, s, "POST", "preview-jobs/"+job.ID+"/cancel", map[string]any{})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"ready"`) {
		t.Fatal("completed job changed by cancel")
	}
}
func TestBackgroundPreviewCancelAndStalePlan(t *testing.T) {
	s := testServer(t)
	p := savedPreviewPlan(t, s)
	s.previews.slot <- struct{}{}
	job, err := s.enqueuePreview(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if w := request(t, s, "POST", "preview-jobs/"+job.ID+"/cancel", map[string]any{}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	finished := waitPreview(t, s, job.ID)
	if finished.State != "cancelled" || finished.Result != nil {
		t.Fatalf("cancel %+v", finished)
	}
	job, err = s.enqueuePreview(p, []string{p.Sources[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	p.Name = "edited while queued"
	s.Store.Put("backup_plans", p.ID, p)
	<-s.previews.slot
	finished = waitPreview(t, s, job.ID)
	if finished.State != "failed" || !strings.Contains(finished.Error, "计划已变更") {
		t.Fatalf("stale %+v", finished)
	}
}
func TestBackgroundPreviewInterruptedRecordAndScopeValidation(t *testing.T) {
	s := testServer(t)
	p := savedPreviewPlan(t, s)
	if w := request(t, s, "POST", "plans/"+p.ID+"/preview-jobs", map[string]any{"sourceIds": []string{"foreign-source"}}); w.Code != 400 {
		t.Fatal(w.Body.String())
	}
	job := previewJob{ID: "scan-interrupted", State: "running", PlanID: p.ID, CreatedAt: time.Now()}
	s.Store.Put("preview_jobs", job.ID, job)
	s.recoverPreviewJobs()
	finished := waitPreview(t, s, job.ID)
	if finished.State != "failed" || !strings.Contains(finished.Error, "服务已重启") {
		t.Fatalf("restart %+v", finished)
	}
}
