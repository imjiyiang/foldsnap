package server

import (
	"encoding/json"
	"errors"
	"localbackup/internal/backup"
)

// Source IDs are normalized in saved-plan order. The persisted plan is never
// modified; callers keep it intact for the queue's configuration-change check.
func scopeSources(p backup.Plan, ids []string) (backup.Plan, []string, error) {
	if len(ids) == 0 {
		return p, nil, nil
	}
	selected := map[string]bool{}
	for _, id := range ids {
		if !backup.ValidID(id) || selected[id] {
			return p, nil, errors.New("目录范围包含重复或无效 ID")
		}
		selected[id] = true
	}
	canonical := []string{}
	enabled := 0
	for _, src := range p.Sources {
		if src.Enabled {
			enabled++
		}
		if selected[src.ID] {
			if !src.Enabled {
				return p, nil, errors.New("所选目录已停用，请重新选择")
			}
			canonical = append(canonical, src.ID)
			delete(selected, src.ID)
		}
	}
	if len(selected) > 0 {
		return p, nil, errors.New("所选目录不属于该计划，请重新选择")
	}
	// Only normalize when there are no other configured sources to preserve.
	if len(canonical) == enabled && enabled == len(p.Sources) {
		return p, nil, nil
	}
	p.Sources = append([]backup.Source{}, p.Sources...)
	selected = map[string]bool{}
	for _, id := range canonical {
		selected[id] = true
	}
	for i := range p.Sources {
		p.Sources[i].Enabled = p.Sources[i].Enabled && selected[p.Sources[i].ID]
	}
	p.ExecutionSourceIDs = canonical
	return p, canonical, nil
}
func scopeJSON(ids []string) string {
	if len(ids) == 0 {
		return "[]"
	}
	data, _ := json.Marshal(ids)
	return string(data)
}
