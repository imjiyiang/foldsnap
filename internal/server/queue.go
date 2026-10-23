package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"localbackup/internal/backup"
	"log"
	"time"
)

func isWaitingStatus(status string) bool {
	return status == "waiting_for_volume" || status == "waiting_for_source"
}
func isRunningStatus(status string) bool {
	return status == "running" || status == "pausing" || status == "paused"
}
func (s *Server) syncControl(id, attempt string, c *backup.Control) {
	s.runSaveMu.Lock()
	defer s.runSaveMu.Unlock()
	var r backup.Run
	if s.Store.Get("backup_runs", id, &r) != nil || r.FinishedAt != nil || !isRunningStatus(r.Status) || r.AttemptID != attempt {
		return
	}
	r.Status = c.State()
	if err := s.Store.Put("backup_runs", id, r); err != nil {
		log.Printf("persist control: %v", err)
	}
}

// dispatchLocked serializes all writers, including restore and retention jobs.
func (s *Server) dispatchLocked() {
	if s.active != "" || s.ctx.Err() != nil {
		return
	}
	var id, planJSON string
	var approvalJSON *string
	err := s.Store.DB.QueryRow("SELECT run_id,plan_data,approval_data FROM run_jobs WHERE state='queued' AND not_before_ns<=? ORDER BY sequence LIMIT 1", time.Now().UnixNano()).Scan(&id, &planJSON, &approvalJSON)
	if err != nil {
		return
	}
	var p backup.Plan
	var approved *previewApproval
	var r backup.Run
	if json.Unmarshal([]byte(planJSON), &p) != nil || s.Store.Get("backup_runs", id, &r) != nil {
		return
	}
	if approvalJSON != nil {
		if err = json.Unmarshal([]byte(*approvalJSON), &approved); err != nil {
			return
		}
	}
	s.runSaveMu.Lock()
	defer s.runSaveMu.Unlock()
	r.AttemptID = backup.ID("attempt-")
	r.Status = "running"
	r.Stage = "预检查与扫描"
	r.StartedAt = time.Now().UTC()
	data, _ := json.Marshal(r)
	tx, err := s.Store.DB.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE backup_runs SET data=? WHERE id=?", string(data), id); err != nil {
		return
	}
	if _, err = tx.Exec("UPDATE run_jobs SET state='running' WHERE run_id=?", id); err != nil {
		return
	}
	if err = tx.Commit(); err != nil {
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	var control *backup.Control
	control = backup.NewControl(func() { s.syncControl(id, r.AttemptID, control) })
	s.active = id
	s.cancel = cancel
	s.control = control
	go func() { defer cancel(); s.execute(backup.WithControl(ctx, control), p, r, approved) }()
}
func (s *Server) queueLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		s.mu.Lock()
		s.dispatchLocked()
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *Server) queueRuns() ([]backup.Run, error) {
	rows, err := s.Store.DB.Query("SELECT backup_runs.data FROM run_jobs JOIN backup_runs ON backup_runs.id=run_jobs.run_id ORDER BY run_jobs.sequence")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []backup.Run{}
	for rows.Next() {
		var data string
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		var r backup.Run
		if err = json.Unmarshal([]byte(data), &r); err != nil {
			return nil, err
		}
		r.Logs = []string{}
		r.Preview = nil
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Server) controlRun(id, action string) (backup.Run, error) {
	s.mu.Lock()
	var r backup.Run
	if err := s.Store.Get("backup_runs", id, &r); err != nil {
		s.mu.Unlock()
		return r, err
	}
	if r.FinishedAt != nil {
		s.mu.Unlock()
		return r, errors.New("任务已结束")
	}
	if s.active == id && s.control != nil && isRunningStatus(r.Status) {
		control, cancel := s.control, s.cancel
		s.mu.Unlock()
		var err error
		switch action {
		case "pause":
			err = control.Pause()
		case "resume":
			err = control.Resume()
		case "cancel":
			cancel()
		}
		if err != nil {
			return r, err
		}
		err = s.Store.Get("backup_runs", id, &r)
		return r, err
	}
	defer s.mu.Unlock()
	s.runSaveMu.Lock()
	var state string
	if err := s.Store.DB.QueryRow("SELECT state FROM run_jobs WHERE run_id=?", id).Scan(&state); err != nil {
		s.runSaveMu.Unlock()
		return r, errors.New("任务当前无法控制")
	}
	if state == "running" {
		s.runSaveMu.Unlock()
		return r, errors.New("运行状态已变化，请刷新")
	}
	next := state
	switch action {
	case "pause":
		next = "held"
		r.Status = "paused"
		r.Stage = "排队任务已暂停"
	case "resume":
		next = "queued"
		r.Status = "queued"
		r.Stage = "排队等待执行"
	case "cancel":
		now := time.Now().UTC()
		r.FinishedAt = &now
		r.Status = "cancelled"
		r.Stage = "排队任务已取消"
	default:
		s.runSaveMu.Unlock()
		return r, fmt.Errorf("未知任务操作: %s", action)
	}
	data, _ := json.Marshal(r)
	tx, err := s.Store.DB.Begin()
	if err == nil {
		if _, err = tx.Exec("UPDATE backup_runs SET data=? WHERE id=?", string(data), id); err == nil {
			if action == "cancel" {
				_, err = tx.Exec("DELETE FROM run_jobs WHERE run_id=?", id)
			} else {
				_, err = tx.Exec("UPDATE run_jobs SET state=?,not_before_ns=0 WHERE run_id=?", next, id)
			}
		}
		if err == nil {
			err = tx.Commit()
		} else {
			tx.Rollback()
		}
	}
	s.runSaveMu.Unlock()
	if err == nil && action == "resume" {
		s.dispatchLocked()
		err = s.Store.Get("backup_runs", id, &r)
	}
	return r, err
}

// Publish the waiting run and retry eligibility atomically. A restart between
// two separate writes must not misclassify a durable waiting job as interrupted.
func (s *Server) persistWaiting(ctx context.Context, r backup.Run) error {
	if err := backup.Checkpoint(ctx); err != nil {
		return err
	}
	s.runSaveMu.Lock()
	defer s.runSaveMu.Unlock()
	tx, err := s.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE backup_runs SET data=? WHERE id=?", string(data), r.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE run_jobs SET state='queued',not_before_ns=? WHERE run_id=?", time.Now().Add(30*time.Second).UnixNano(), r.ID); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return tx.Commit()
}
