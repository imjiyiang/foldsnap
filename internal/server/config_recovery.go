package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"localbackup/internal/backup"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type recoveryView struct {
	PreviewID string        `json:"previewId"`
	ExpiresAt time.Time     `json:"expiresAt"`
	Plans     []backup.Plan `json:"plans"`
	Snapshots int           `json:"snapshots"`
	Pinned    int           `json:"pinned"`
	Warnings  []string      `json:"warnings"`
}
type recoveryGrant struct {
	View          recoveryView
	Destination   backup.Destination
	Digest        string
	LocalRevision string
}

// Ephemeral, one-use recovery previews are invalid after process restart.
func localRecoveryRevision(ctx context.Context, db interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) (string, error) {
	h := sha256.New()
	rows, err := db.QueryContext(ctx, "SELECT 'plan',id,data FROM backup_plans UNION ALL SELECT 'snapshot',id,data FROM snapshots ORDER BY 1,2")
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, id, data string
		if err = rows.Scan(&kind, &id, &data); err != nil {
			return "", err
		}
		json.NewEncoder(h).Encode([]string{kind, id, data})
	}
	return hex.EncodeToString(h.Sum(nil)), rows.Err()
}
func configConflict(p backup.Plan, existing []backup.Plan, dataDir string) error {
	for _, src := range p.Sources {
		if backup.Contains(dataDir, src.Path) {
			return fmt.Errorf("源位于服务数据目录")
		}
	}
	for _, other := range existing {
		if p.ID == other.ID {
			return fmt.Errorf("已有同 ID 计划，保留现有配置")
		}
		for _, src := range p.Sources {
			for _, osrc := range other.Sources {
				if strings.EqualFold(src.ID, osrc.ID) {
					return fmt.Errorf("源 ID 已属于其他计划")
				}
			}
			for _, dest := range other.Destinations {
				if backup.PathsOverlap(src.Path, dest.Path) {
					return fmt.Errorf("源与已有计划的目标重叠")
				}
			}
		}
		for _, dest := range p.Destinations {
			for _, src := range other.Sources {
				if backup.PathsOverlap(dest.Path, src.Path) {
					return fmt.Errorf("目标与已有计划的源重叠")
				}
			}
		}
	}
	return nil
}

// The repository shared lock must be held throughout inspection and commit.
func (s *Server) inspectRecovery(ctx context.Context, d backup.Destination) (recoveryView, []backup.Snapshot, string, error) {
	out := recoveryView{Plans: []backup.Plan{}, Warnings: []string{}}
	repo, root, err := backup.OpenRepository(d, false)
	if err != nil {
		return out, nil, "", err
	}
	dirs, err := os.ReadDir(filepath.Join(root, "plans"))
	if err != nil {
		return out, nil, "", err
	}
	snapshots := []backup.Snapshot{}
	latest := map[string]backup.Snapshot{}
	h := sha256.New()
	unknownPins := 0
	for _, plan := range dirs {
		if !plan.IsDir() || !backup.ValidID(plan.Name()) {
			continue
		}
		base := filepath.Join(root, "plans", plan.Name(), "snapshots")
		if err = backup.SafeTree(root, base); err != nil {
			return out, nil, "", err
		}
		entries, er := os.ReadDir(base)
		if os.IsNotExist(er) {
			continue
		}
		if er != nil {
			return out, nil, "", er
		}
		for _, item := range entries {
			if err = backup.Checkpoint(ctx); err != nil {
				return out, nil, "", err
			}
			if !item.IsDir() || !backup.ValidID(item.Name()) {
				continue
			}
			path := filepath.Join(base, item.Name())
			file := filepath.Join(path, "manifest.json")
			if err = backup.SafeTree(root, file); err != nil {
				return out, nil, "", err
			}
			data, er := backup.ReadMetadataFile(file, 1<<30)
			if er != nil {
				return out, nil, "", er
			}
			var sn backup.Snapshot
			if json.Unmarshal(data, &sn) != nil || sn.ID != item.Name() || sn.PlanID != plan.Name() || sn.RepositoryID != repo.ID || sn.VolumeID != d.VolumeID {
				return out, nil, "", fmt.Errorf("仓库清单身份不匹配: %s", item.Name())
			}
			actual, er := backup.ValidateSnapshot(sn)
			if er != nil || actual != path {
				return out, nil, "", fmt.Errorf("快照路径校验失败: %s", sn.ID)
			}
			if er = backup.AuditSnapshot(ctx, sn); er != nil {
				return out, nil, "", er
			}
			pinned, known, pinErr := backup.ReadSnapshotPin(root, sn)
			if pinErr != nil || !known {
				pinned = true
				unknownPins++
			}
			sn.Pinned = sn.Pinned || pinned
			if sn.Pinned {
				out.Pinned++
			}
			json.NewEncoder(h).Encode([]any{sn.ID, string(data), sn.Pinned})
			snapshots = append(snapshots, sn)
			if sn.PlanConfig != nil && (latest[sn.PlanID].ID == "" || sn.CreatedAt.After(latest[sn.PlanID].CreatedAt)) {
				latest[sn.PlanID] = sn
			}
		}
	}
	if unknownPins > 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf("%d 份快照的磁盘保留标记未知或损坏，将按永久保留导入", unknownPins))
	}
	current, err := s.Store.Plans()
	if err != nil {
		return out, nil, "", err
	}
	ids := []string{}
	for id := range latest {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		sn := latest[id]
		p := *sn.PlanConfig
		p.Enabled = false
		if p.ID != id {
			return out, nil, "", fmt.Errorf("计划与清单身份不一致")
		}
		if err = backup.ValidateRecoveryPlan(p); err != nil {
			out.Warnings = append(out.Warnings, p.Name+": "+err.Error())
			continue
		}
		matched := false
		for _, dest := range p.Destinations {
			if dest.ID == sn.DestinationID && dest.VolumeID == sn.VolumeID {
				matched = true
			}
		}
		if !matched {
			out.Warnings = append(out.Warnings, p.Name+": 清单目标与计划绑定不一致")
			continue
		}
		if err = configConflict(p, current, filepath.Dir(s.Store.Path)); err != nil {
			out.Warnings = append(out.Warnings, p.Name+": "+err.Error())
			continue
		}
		out.Plans = append(out.Plans, p)
		current = append(current, p)
	}
	planSet := map[string]bool{}
	for _, sn := range snapshots {
		if sn.PlanConfig == nil && !planSet[sn.PlanID] {
			out.Warnings = append(out.Warnings, "旧清单没有完整计划，仅恢复历史: "+sn.PlanName)
			planSet[sn.PlanID] = true
		}
	}
	out.Snapshots = len(snapshots)
	return out, snapshots, hex.EncodeToString(h.Sum(nil)), nil
}
func (s *Server) recoveryAPI(w http.ResponseWriter, r *http.Request, apply bool) {
	var req struct {
		Path         string `json:"path"`
		PreviewID    string `json:"previewId"`
		RestorePlans bool   `json:"restorePlans"`
	}
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	if s.active != "" {
		s.mu.Unlock()
		fail(w, 409, "已有文件任务正在执行")
		return
	}
	s.active = "recovery"
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.active = ""; s.dispatchLocked(); s.mu.Unlock() }()
	var grant recoveryGrant
	var d backup.Destination
	if apply {
		s.mu.Lock()
		saved, ok := s.recoveries[req.PreviewID]
		delete(s.recoveries, req.PreviewID)
		s.mu.Unlock()
		if !ok || time.Now().After(saved.View.ExpiresAt) {
			fail(w, 409, "恢复预览已失效，请重新选择备份目录")
			return
		}
		grant = saved
		d = grant.Destination
	} else {
		path, err := backup.Canonical(req.Path)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		volume, err := backup.VolumeID(path)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		d = backup.Destination{Path: path, VolumeID: volume}
	}
	unlock, err := backup.LockRepository(d, false, false)
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	defer unlock()
	view, snapshots, digest, err := s.inspectRecovery(r.Context(), d)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	revision, err := localRecoveryRevision(r.Context(), s.Store.DB)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if !apply {
		view.PreviewID = backup.ID("recovery-")
		view.ExpiresAt = time.Now().Add(10 * time.Minute)
		s.mu.Lock()
		if s.recoveries == nil {
			s.recoveries = map[string]recoveryGrant{}
		}
		for id, g := range s.recoveries {
			if time.Now().After(g.View.ExpiresAt) {
				delete(s.recoveries, id)
			}
		}
		s.recoveries[view.PreviewID] = recoveryGrant{view, d, digest, revision}
		s.mu.Unlock()
		reply(w, 200, view)
		return
	}
	if digest != grant.Digest || revision != grant.LocalRevision || fingerprint(view.Plans) != fingerprint(grant.View.Plans) {
		fail(w, 409, "备份盘或本地配置在预览后发生变化，请重新预览")
		return
	}
	tx, err := s.Store.DB.BeginTx(r.Context(), nil)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	defer tx.Rollback()
	inside, err := localRecoveryRevision(r.Context(), tx)
	if err != nil || inside != revision {
		fail(w, 409, "本地记录在恢复前发生变化")
		return
	}
	restoredPlans := 0
	newPlans := map[string]bool{}
	newSources := map[string]map[string]bool{}
	if req.RestorePlans {
		for _, p := range view.Plans {
			p.Enabled = false
			data, _ := json.Marshal(p)
			if _, err = tx.ExecContext(r.Context(), "INSERT INTO backup_plans VALUES(?,?)", p.ID, string(data)); err != nil {
				fail(w, 400, err.Error())
				return
			}
			restoredPlans++
			newPlans[p.ID] = true
			newSources[p.ID] = map[string]bool{}
			for _, src := range p.Sources {
				newSources[p.ID][src.ID] = true
			}
		}
	}
	imported := 0
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].CreatedAt.Before(snapshots[j].CreatedAt) })
	for _, sn := range snapshots {
		var existing string
		er := tx.QueryRowContext(r.Context(), "SELECT data FROM snapshots WHERE id=?", sn.ID).Scan(&existing)
		if er == nil {
			var old backup.Snapshot
			if json.Unmarshal([]byte(existing), &old) != nil || old.RepositoryID != sn.RepositoryID || old.PlanID != sn.PlanID {
				fail(w, 409, "已有快照身份冲突")
				return
			}
			pinned := sn.Pinned || old.Pinned
			old.Pinned = sn.Pinned
			if backup.SnapshotDigest(old) != backup.SnapshotDigest(sn) {
				fail(w, 409, "已知快照清单与磁盘内容不一致，保留本地记录")
				return
			}
			sn.Pinned = pinned
		} else if er != sql.ErrNoRows {
			fail(w, 400, er.Error())
			return
		} else {
			imported++
		}
		data, _ := json.Marshal(sn)
		if _, err = tx.ExecContext(r.Context(), "INSERT INTO snapshots VALUES(?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data", sn.ID, string(data)); err != nil {
			fail(w, 400, err.Error())
			return
		}
		if newPlans[sn.PlanID] {
			for _, src := range sn.Sources {
				if newSources[sn.PlanID][src.ID] {
					if _, err = tx.ExecContext(r.Context(), "DELETE FROM file_entries WHERE source_id=?", src.ID); err != nil {
						fail(w, 400, err.Error())
						return
					}
				}
			}
			for _, en := range sn.Entries {
				if !newSources[sn.PlanID][en.SourceID] {
					continue
				}
				data, _ = json.Marshal(en)
				if _, err = tx.ExecContext(r.Context(), "INSERT OR REPLACE INTO file_entries VALUES(?,?,?)", en.SourceID, en.Path, string(data)); err != nil {
					fail(w, 400, err.Error())
					return
				}
			}
		}
	}
	auditID := backup.ID("recovery-job-")
	audit, _ := json.Marshal(map[string]any{"id": auditID, "operation": "metadata-recovery", "status": "success", "previewId": grant.View.PreviewID, "path": d.Path, "restoredPlans": restoredPlans, "importedSnapshots": imported, "pinnedSnapshots": view.Pinned, "finishedAt": time.Now().UTC(), "warnings": view.Warnings})
	if _, err = tx.ExecContext(r.Context(), "INSERT INTO restore_jobs VALUES(?,?)", auditID, string(audit)); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if err = backup.Checkpoint(r.Context()); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if err = tx.Commit(); err != nil {
		fail(w, 400, err.Error())
		return
	}
	reply(w, 200, map[string]any{"imported": imported, "restoredPlans": restoredPlans, "pinned": view.Pinned, "warnings": view.Warnings, "auditId": auditID})
}
