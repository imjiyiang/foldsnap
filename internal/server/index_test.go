package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"localbackup/internal/backup"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIndexAPICacheStatsAndForcedAudit(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	run := apiRun(t, s, p, RunRequest{})
	if run.Status != "success" {
		t.Fatal(run.Error)
	}
	status, err := s.Store.IndexStatus(context.Background())
	if err != nil || status.Paths != 2 || len(status.Sources) != 2 {
		t.Fatalf("missing index: %+v %v", status, err)
	}
	v := getPreview(t, s, p)
	if v.ScanStats == nil {
		t.Fatal("preview omitted scan stats")
	}
	if status.Sources[0].Cacheable && v.ScanStats.CacheHits != 2 {
		t.Fatal("unchanged preview did not reuse hashes", v.ScanStats)
	}
	if status.Objects > 0 {
		forgeIndexHashes(t, s)
		differs, err := s.contentChanged(context.Background(), p)
		if err != nil || differs {
			t.Fatal("gap audit trusted cache", err)
		}
		forgeIndexHashes(t, s)
		differs, err = s.contentChanged(context.Background(), p, false)
		if err != nil || !differs {
			t.Fatal("normal scan cache fixture not exercised", err)
		}
	}
	w := request(t, s, "POST", "system/index/scan", map[string]string{"planId": p.ID})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var response struct {
		Stats backup.ScanStats `json:"scanStats"`
	}
	json.Unmarshal(w.Body.Bytes(), &response)
	if !response.Stats.Forced || response.Stats.FilesHashed != 2 || response.Stats.CacheHits != 0 {
		t.Fatal("full rescan reused stale cache", response)
	}
	differs, err := s.contentChanged(context.Background(), p)
	if err != nil || differs {
		t.Fatal("forced rescan did not repair source index", err)
	}
	s.mu.Lock()
	s.active = "restore"
	s.mu.Unlock()
	w = request(t, s, "POST", "system/index/scan", map[string]string{"planId": p.ID})
	if w.Code != 409 {
		t.Fatal("index maintenance entered occupied file slot")
	}
}
func TestIndexDiagnosticsRequireAuthentication(t *testing.T) {
	s := testServer(t)
	r := httptest.NewRequest("GET", "http://127.0.0.1:8787/api/v1/system/index", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("index leaked without token", w.Code)
	}
}
func TestDraftRuleTestDoesNotReplaceSavedSourceIndex(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	if r := apiRun(t, s, p, RunRequest{}); r.Status != "success" {
		t.Fatal(r.Error)
	}
	before, err := s.Store.IndexStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p.Rules = []backup.Rule{{Action: "exclude", Pattern: "**"}}
	response := request(t, s, "POST", "plans/"+p.ID+"/rules/preview", p)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	after, err := s.Store.IndexStatus(context.Background())
	if err != nil || after.Paths != before.Paths || after.Excluded != before.Excluded || after.Sources[0].Generation != before.Sources[0].Generation {
		t.Fatal("unsaved rule test replaced committed index", err)
	}
}
func TestSourceIDsCannotAliasAnotherPlanIndex(t *testing.T) {
	s := testServer(t)
	first := saveFixturePlan(t, s, "snapshot")
	second := planFixture(t)
	second.Sources[0].ID = first.Sources[0].ID
	response := request(t, s, "POST", "plans", second)
	if response.Code != 400 || !strings.Contains(response.Body.String(), "ID") {
		t.Fatal("cross-plan index namespace collision accepted", response.Body.String())
	}
}
func TestPollingFallbackRunsFullAuditWithoutResettingItsClock(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	r := apiRun(t, s, p, RunRequest{})
	if r.Status != "success" {
		t.Fatal(r.Error)
	}
	p.Schedule.OnChange = true
	p.Schedule.IntervalMinutes = 0
	p.Schedule.OnMount = false
	s.Store.Put("backup_plans", p.ID, p)
	sig, err := signature(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	s.Store.Put("scan_checkpoints", p.ID, checkpoint{Signature: sig, LastAudit: time.Now().Add(-2 * time.Hour)})
	before, _ := s.Store.IndexStatus(context.Background())
	s.Store.DB.Exec("UPDATE filesystem_objects SET content_hash=?", strings.Repeat("0", 64))
	s.tick(context.Background())
	after, err := s.Store.IndexStatus(context.Background())
	if err != nil || after.Sources[0].Generation <= before.Sources[0].Generation {
		t.Fatal("polling full audit never executed", err)
	}
	var first, second checkpoint
	s.Store.Get("scan_checkpoints", p.ID, &first)
	s.tick(context.Background())
	s.Store.Get("scan_checkpoints", p.ID, &second)
	if first.LastAudit.IsZero() || !first.LastAudit.Equal(second.LastAudit) {
		t.Fatal("cheap poll reset full-audit clock")
	}
}
func TestPollingSignatureSeesRestoredMtimeMutation(t *testing.T) {
	s := testServer(t)
	p := saveFixturePlan(t, s, "snapshot")
	path := filepath.Join(p.Sources[0].Path, "one.txt")
	st, _ := os.Stat(path)
	before, err := signature(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte("edited bytes"), 0644)
	os.Chtimes(path, st.ModTime(), st.ModTime())
	latest, _ := os.Stat(path)
	if backup.MetadataVersion(st) == backup.MetadataVersion(latest) {
		t.Skip("filesystem does not expose a changed metadata version")
	}
	after, err := signature(context.Background(), p)
	if err != nil || before == after {
		t.Fatal("metadata version not included in polling signature", err)
	}
}
func TestConcurrentPlansCannotShareSourceIndexNamespace(t *testing.T) {
	s := testServer(t)
	plans := []backup.Plan{}
	for i := 0; i < 4; i++ {
		p := planFixture(t)
		p.Sources[0].ID = "src-shared"
		plans = append(plans, p)
	}
	start := make(chan struct{})
	codes := make(chan int, 4)
	for _, p := range plans {
		go func(p backup.Plan) { <-start; codes <- request(t, s, "POST", "plans", p).Code }(p)
	}
	close(start)
	created := 0
	for range plans {
		code := <-codes
		if code == 201 {
			created++
		} else if code != 400 {
			t.Fatal("unexpected creation result", code)
		}
	}
	if created != 1 {
		t.Fatal("duplicate global source identities persisted", created)
	}
}

func forgeIndexHashes(t *testing.T, s *Server) {
	t.Helper()
	key, err := os.ReadFile(filepath.Join(filepath.Dir(s.Store.Path), "fingerprint-integrity.key"))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.Store.DB.Query("SELECT object_key FROM filesystem_objects")
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for rows.Next() {
		var k string
		rows.Scan(&k)
		keys = append(keys, k)
	}
	rows.Close()
	hash := strings.Repeat("0", 64)
	for _, k := range keys {
		m := hmac.New(sha256.New, key)
		m.Write([]byte("fingerprint-v1\x00" + k + "\x00" + hash))
		tag := hex.EncodeToString(m.Sum(nil))
		if _, err = s.Store.DB.Exec("UPDATE filesystem_objects SET content_hash=?,auth_tag=? WHERE object_key=?", hash, tag, k); err != nil {
			t.Fatal(err)
		}
	}
}
