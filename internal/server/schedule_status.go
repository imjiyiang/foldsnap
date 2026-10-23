package server

import (
	"localbackup/internal/backup"
	"time"
)

func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
func (s *Server) scheduleStatus(p backup.Plan) map[string]any {
	var cp checkpoint
	s.Store.Get("scan_checkpoints", p.ID, &cp)
	decision := cp.Decision
	if !p.Enabled {
		decision = "自动备份已暂停"
	} else if !p.Schedule.OnChange && !p.Schedule.OnMount && p.Schedule.IntervalMinutes == 0 && p.Schedule.MaxIntervalMinutes == 0 {
		decision = "仅手动触发"
	} else if decision == "" {
		decision = "等待首次调度检查"
	}
	var next *time.Time
	if cp.RetryPending && cp.Failures < p.Schedule.AutomaticAttempts() && !cp.LastAttempt.IsZero() {
		next = optionalTime(cp.LastAttempt.Add(retryDelay(cp.Failures)))
	}
	return map[string]any{"planId": p.ID, "enabled": p.Enabled, "decision": decision, "evaluatedAt": optionalTime(cp.LastEvaluatedAt), "checkedAt": optionalTime(cp.LastChangeCheck), "lastMaxCheck": optionalTime(cp.LastMaxCheck), "changes": cp.Changes, "failures": cp.Failures, "maxAttempts": p.Schedule.AutomaticAttempts(), "retryPending": cp.RetryPending, "nextRetryAt": next}
}
