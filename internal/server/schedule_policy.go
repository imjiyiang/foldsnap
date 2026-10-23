package server

import (
	"context"
	"fmt"
	"localbackup/internal/backup"
	"math"
	"time"
)

type ChangeSummary struct {
	Changed     bool  `json:"changed"`
	HasBaseline bool  `json:"hasBaseline"`
	Files       int   `json:"files"`
	Bytes       int64 `json:"bytes"`
}

func retryDelay(failures int) time.Duration {
	exponent := failures - 1
	if exponent < 0 {
		exponent = 0
	}
	if exponent > 6 {
		exponent = 6
	}
	delay := time.Minute * time.Duration(1<<exponent)
	if delay > time.Hour {
		delay = time.Hour
	}
	return delay
}
func maximumCheckDue(p backup.Plan, cp checkpoint, successAt, now time.Time) bool {
	if p.Schedule.MaxIntervalMinutes <= 0 {
		return false
	}
	reference := successAt
	if reference.IsZero() {
		reference = p.CreatedAt
		if reference.IsZero() {
			reference = cp.FirstObservedAt
		}
	}
	if cp.LastMaxCheck.After(reference) {
		reference = cp.LastMaxCheck
	}
	return !reference.IsZero() && now.Sub(reference) >= time.Duration(p.Schedule.MaxIntervalMinutes)*time.Minute
}
func thresholdReached(s backup.Schedule, summary ChangeSummary) bool {
	return summary.Changed && summary.Files >= s.MinChangedFiles && summary.Bytes >= s.MinChangedBytes
}

// Counts are net differences from each destination's intended view, never the
// number of FSEvents. Union by source/path avoids counting target fan-out twice.
func (s *Server) changeSummary(ctx context.Context, p backup.Plan, force bool) (ChangeSummary, error) {
	result := ChangeSummary{}
	p = s.scanPlan(p)
	var err error
	p, err = s.resolveSources(p)
	if err != nil {
		return result, err
	}
	p.EvaluatedAt = backup.EvaluationTime(p)
	p, err = s.withDirtyHints(ctx, p, force)
	if err != nil {
		return result, err
	}
	scan, err := backup.ScanIndexed(ctx, p, s.Store, force)
	if err != nil {
		return result, err
	}
	changedPaths := map[string]int64{}
	modern := backup.DetectRsync(s.config().RsyncPath).Modern
	for _, d := range p.Destinations {
		if !d.Enabled {
			continue
		}
		previous, err := s.baseline(p, d)
		if err != nil {
			return result, err
		}
		var before []backup.Entry
		if previous != nil {
			before = previous.Entries
			result.HasBaseline = true
		}
		view := backup.BuildView(p, scan.Entries, previous)
		if previous == nil || !backup.EquivalentTrees(view.Entries, before) || (modern && !previous.ExtendedMetadata) {
			result.Changed = true
		}
		collectChangedPaths(changedPaths, view.Entries, before)
	}
	result.Files = len(changedPaths)
	for _, size := range changedPaths {
		if size > math.MaxInt64-result.Bytes {
			result.Bytes = math.MaxInt64
			break
		}
		result.Bytes += size
	}
	return result, nil
}
func collectChangedPaths(changed map[string]int64, after, before []backup.Entry) {
	old := map[string]backup.Entry{}
	for _, en := range before {
		old[en.SourceID+"/"+en.Path] = en
	}
	for _, en := range after {
		key := en.SourceID + "/" + en.Path
		previous, exists := old[key]
		if !exists || !backup.EquivalentTrees([]backup.Entry{en}, []backup.Entry{previous}) {
			if en.Kind != "directory" || (exists && previous.Kind != "directory") {
				size := int64(0)
				if en.Kind != "directory" && en.Size > size {
					size = en.Size
				}
				if exists && previous.Kind != "directory" && previous.Size > size {
					size = previous.Size
				}
				if current, ok := changed[key]; !ok || size > current {
					changed[key] = size
				}
			}
		}
		delete(old, key)
	}
	for key, en := range old {
		if en.Kind != "directory" {
			size := en.Size
			if size < 0 {
				size = 0
			}
			if current, ok := changed[key]; !ok || size > current {
				changed[key] = size
			}
		}
	}
}

func (s *Server) evaluateChanges(ctx context.Context, p backup.Plan, cp *checkpoint, successAt time.Time, successRunID string, maxDue bool) (bool, error) {
	if !p.Schedule.OnChange && !maxDue && !cp.MaxBackupPending {
		cp.Decision = "等待最长检查间隔"
		return false, nil
	}
	pending, err := s.pending(p.ID)
	if err != nil {
		return false, err
	}
	now := time.Now()
	debounce := time.Duration(p.Schedule.QuietSeconds()) * time.Second
	quiet := pending.ChangedAt.IsZero() || now.Sub(pending.ChangedAt) >= debounce
	native := s.nativeHealthy()
	auditDue := cp.LastAudit.IsZero() || now.Sub(cp.LastAudit) >= time.Hour
	baselineChanged := cp.ChangeRunID != successRunID
	newQuietRevision := pending.Count > 0 && quiet && (pending.Revision != cp.ChangeRevision || (!pending.ChangedAt.IsZero() && cp.LastChangeCheck.Before(pending.ChangedAt.Add(debounce))))
	shouldObserve := maxDue || !native || auditDue || baselineChanged || newQuietRevision || cp.LastChangeCheck.IsZero()
	if shouldObserve {
		// Native revisions replace the polling tree walk. The content scan still
		// falls back to a full traversal whenever its signed hints are untrusted.
		sig := fmt.Sprintf("native:%d", pending.Revision)
		if !native {
			sig, err = signature(ctx, p)
			if err != nil {
				return false, err
			}
		}
		if cp.Signature == "" {
			cp.Signature = sig
		} else if cp.Signature != sig {
			cp.Signature = sig
			cp.ChangedAt = now
			if !pending.ChangedAt.IsZero() {
				cp.ChangedAt = pending.ChangedAt
			}
		}
		stableChange := !cp.ChangedAt.IsZero() && now.Sub(cp.ChangedAt) >= debounce
		needsSummary := maxDue || auditDue || baselineChanged || cp.LastChangeCheck.IsZero() || (quiet && (newQuietRevision || cp.ChangeSignature != sig || stableChange && cp.Changes == nil))
		if needsSummary {
			force := maxDue || auditDue || pending.NeedsScan || !native
			summary, err := s.changeSummary(ctx, p, force)
			if err != nil {
				return false, err
			}
			cp.Changes = &summary
			cp.LastChangeCheck = time.Now()
			cp.ChangeRevision = pending.Revision
			cp.ChangeSignature = sig
			cp.ChangeRunID = successRunID
			if force && (auditDue || pending.NeedsScan || maxDue) {
				cp.LastAudit = cp.LastChangeCheck
			}
			if maxDue {
				cp.LastMaxCheck = cp.LastChangeCheck
				cp.MaxBackupPending = summary.Changed
			}
			if !summary.Changed {
				cp.ChangedAt = time.Time{}
				cp.MaxBackupPending = false
				if pending.Count > 0 {
					if err = s.ackChanges(p.ID, pending.Revision); err != nil {
						return false, err
					}
				}
			} else if cp.ChangedAt.IsZero() {
				switch {
				case !pending.ChangedAt.IsZero():
					cp.ChangedAt = pending.ChangedAt
				case summary.HasBaseline || !successAt.IsZero():
					cp.ChangedAt = now
				}
			}
		}
	}
	if cp.MaxBackupPending {
		cp.Decision = "最长间隔检查发现变化"
		return false, nil
	}
	if !p.Schedule.OnChange {
		cp.Decision = "等待最长检查间隔"
		return false, nil
	}
	if cp.Changes == nil || !cp.Changes.Changed || cp.ChangedAt.IsZero() {
		cp.Decision = "未发现需要备份的稳定变化"
		return false, nil
	}
	if !quiet || now.Sub(cp.ChangedAt) < debounce {
		cp.Decision = "等待文件变化稳定"
		return false, nil
	}
	if !thresholdReached(p.Schedule, *cp.Changes) {
		cp.Decision = "已记录变化，尚未同时达到启用的数量和大小阈值"
		return false, nil
	}
	if !successAt.IsZero() && now.Sub(successAt) < time.Duration(p.Schedule.ChangeIntervalMinutes())*time.Minute {
		cp.Decision = "等待变化触发的最短间隔"
		return false, nil
	}
	cp.Decision = "稳定变化已达到触发条件"
	return true, nil
}
