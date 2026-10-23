package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"localbackup/internal/backup"
)

type RunRequest struct {
	AutomaticAttempt int      `json:"-"`
	RetryOf          string   `json:"-"`
	SourceIDs        []string `json:"sourceIds,omitempty"`
	// Only the scheduler may scope a run; HTTP clients cannot bypass full-plan approvals.
	DestinationIDs []string `json:"-"`
	PreviewID      string   `json:"previewId,omitempty"`
	ConfirmDeletes bool     `json:"confirmDeletes"`
}
type previewApproval struct {
	SourceIDs     []string  `json:"sourceIds,omitempty"`
	ID            string    `json:"id"`
	PlanID        string    `json:"planId"`
	Fingerprint   string    `json:"fingerprint"`
	DeletePaths   int       `json:"deletePaths"`
	DeleteBytes   int64     `json:"deleteBytes"`
	ExpiresAt     time.Time `json:"expiresAt"`
	ConsumedRunID string    `json:"consumedRunId,omitempty"`
}
type confirmationRequired struct{ reason string }

func (e confirmationRequired) Error() string { return e.reason }
func (s *Server) baseline(p backup.Plan, d backup.Destination) (*backup.Snapshot, error) {
	if p.Mode == "incremental" || p.Mode == "mirror" {
		current, e := backup.ReadCurrent(d, p.ID)
		if e != nil {
			return nil, e
		}
		if current != nil {
			return current, nil
		}
	}
	return s.Store.LatestSnapshot(s.ctx, p.ID, d.ID)
}
func (s *Server) createPreview(ctx context.Context, p backup.Plan, sourceIDs []string) (backup.Preview, error) {
	var err error
	p, sourceIDs, err = scopeSources(p, sourceIDs)
	if err != nil {
		return backup.Preview{}, err
	}
	v, _, e := s.previewWithPreflight(ctx, p, true)
	if e != nil {
		return v, e
	}
	id := backup.ID("preview-")
	expires := time.Now().UTC().Add(10 * time.Minute)
	approval := previewApproval{SourceIDs: sourceIDs, ID: id, PlanID: p.ID, Fingerprint: v.Fingerprint, DeletePaths: v.DeletePaths, DeleteBytes: v.DeleteBytes, ExpiresAt: expires}
	if e = s.Store.Put("backup_previews", id, approval); e != nil {
		return v, e
	}
	v.PreviewID = id
	v.ExpiresAt = &expires
	return v, nil
}
func fingerprint(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (s *Server) startWithRequest(p backup.Plan, trigger string, req RunRequest) (backup.Run, error) {
	_, canonical, scopeErr := scopeSources(p, req.SourceIDs)
	if scopeErr != nil {
		return backup.Run{}, scopeErr
	}
	req.SourceIDs = canonical
	if len(canonical) > 0 && (trigger != "manual" || len(req.DestinationIDs) > 0) {
		return backup.Run{}, errors.New("单目录范围仅支持手动备份")
	}

	// BEGIN IMMEDIATE serializes queue/grant mutations without holding the run
	// control mutex while another index transaction owns the SQLite writer slot.
	tx, err := s.Store.DB.Begin()
	if err != nil {
		return backup.Run{}, err
	}
	defer tx.Rollback()
	var existingData string
	err = tx.QueryRow(`SELECT r.data FROM run_jobs j JOIN backup_runs r ON r.id=j.run_id
 WHERE json_extract(j.plan_data,'$.id')=? AND json_extract(r.data,'$.finishedAt') IS NULL
 AND coalesce(json_extract(r.data,'$.sourceIds'),'[]')=?
 AND coalesce(json_extract(r.data,'$.destinationIds'),'[]')=?`, p.ID, scopeJSON(req.SourceIDs), scopeJSON(req.DestinationIDs)).Scan(&existingData)
	if err == nil {
		var existing backup.Run
		if err = json.Unmarshal([]byte(existingData), &existing); err != nil {
			return backup.Run{}, err
		}
		if existing.FinishedAt == nil {
			return existing, nil
		}
		if _, err = tx.Exec("DELETE FROM run_jobs WHERE run_id=?", existing.ID); err != nil {
			return backup.Run{}, err
		}
	} else if err != sql.ErrNoRows {
		return backup.Run{}, err
	}
	var count int
	if err = tx.QueryRow("SELECT count(*) FROM run_jobs").Scan(&count); err != nil {
		return backup.Run{}, err
	}
	if count >= 100 {
		return backup.Run{}, errors.New("任务队列已满，请先处理已有任务")
	}
	var approved *previewApproval
	if req.PreviewID != "" {
		var data string
		var a previewApproval
		if err = tx.QueryRow("SELECT data FROM backup_previews WHERE id=?", req.PreviewID).Scan(&data); err != nil {
			return backup.Run{}, errors.New("预览不存在，请重新预览")
		}
		if err = json.Unmarshal([]byte(data), &a); err != nil {
			return backup.Run{}, err
		}
		if a.PlanID != p.ID || scopeJSON(a.SourceIDs) != scopeJSON(req.SourceIDs) || time.Now().After(a.ExpiresAt) || a.ConsumedRunID != "" {
			return backup.Run{}, errors.New("预览已过期或已使用，请重新预览")
		}
		if p.Mode == "mirror" && a.DeletePaths > 0 && !req.ConfirmDeletes {
			return backup.Run{}, errors.New("镜像删除需要明确确认")
		}
		approved = &a
	}
	now := time.Now().UTC()
	r := backup.Run{AutomaticAttempt: req.AutomaticAttempt, RetryOf: req.RetryOf, SourceIDs: req.SourceIDs, DestinationIDs: req.DestinationIDs, ID: backup.ID("run-"), PlanID: p.ID, PlanName: p.Name, Status: "queued", Stage: "排队等待执行", QueuedAt: &now, StartedAt: now, Logs: []string{}, Trigger: trigger, DeletesConfirmed: req.ConfirmDeletes, ApprovedPreviewID: req.PreviewID}
	for _, src := range p.Sources {
		for _, id := range req.SourceIDs {
			if src.ID == id {
				r.SourceNames = append(r.SourceNames, src.Name)
			}
		}
	}
	var approvalData any
	if approved != nil {
		approved.ConsumedRunID = r.ID
		b, _ := json.Marshal(approved)
		approvalData = string(b)
		if _, err = tx.Exec("UPDATE backup_previews SET data=? WHERE id=?", approvalData, approved.ID); err != nil {
			return r, err
		}
	}
	data, _ := json.Marshal(r)
	if _, err = tx.Exec("INSERT INTO backup_runs VALUES(?,?)", r.ID, string(data)); err != nil {
		return r, err
	}
	planData, _ := json.Marshal(p)
	if _, err = tx.Exec("INSERT INTO run_jobs(run_id,plan_data,approval_data,state) VALUES(?,?,?,'queued')", r.ID, string(planData), approvalData); err != nil {
		return r, err
	}
	if err = tx.Commit(); err != nil {
		return r, err
	}
	s.mu.Lock()
	s.dispatchLocked()
	s.mu.Unlock()
	s.Store.Get("backup_runs", r.ID, &r)
	return r, nil
}

func validateApproval(p backup.Plan, v backup.Preview, approved *previewApproval, confirmed bool) error {
	if approved != nil && (v.Fingerprint != approved.Fingerprint || scopeJSON(v.SourceIDs) != scopeJSON(approved.SourceIDs)) {
		return confirmationRequired{"目录、规则或目标视图已变化，原预览失效；请重新预览后确认"}
	}
	if p.Mode == "mirror" && v.DeletePaths > 0 {
		if approved == nil || !confirmed {
			return confirmationRequired{fmt.Sprintf("镜像需要从同步视图移除 %d 项（%d 字节），请查看预览并明确确认；历史版本保留", v.DeletePaths, v.DeleteBytes)}
		}
	}
	return nil
}
