package server

import (
	"errors"
	"localbackup/internal/backup"
)

// Persist transitions per destination: one absent rotation disk must not block
// another. Pending insertions survive a busy queue and are consumed on enqueue.
func observeMounts(cp *checkpoint, p backup.Plan, check func(backup.Destination) (string, error)) {
	previous := cp.TargetOnline
	cp.TargetOnline = map[string]bool{}
	if cp.PendingMounts == nil {
		cp.PendingMounts = map[string]bool{}
	}
	cp.Online = true
	enabled := 0
	for _, d := range p.Destinations {
		if !d.Enabled {
			delete(cp.PendingMounts, d.ID)
			continue
		}
		enabled++
		_, err := check(d)
		online := err == nil
		cp.TargetOnline[d.ID] = online
		if !online {
			cp.Online = false
			delete(cp.PendingMounts, d.ID)
		}
		if online && !previous[d.ID] && p.Schedule.OnMount {
			cp.PendingMounts[d.ID] = true
		}
	}
	if enabled == 0 {
		cp.Online = false
	}
	for id := range cp.PendingMounts {
		if !cp.TargetOnline[id] || !p.Schedule.OnMount {
			delete(cp.PendingMounts, id)
		}
	}
}
func pendingMountIDs(cp checkpoint, p backup.Plan) []string {
	ids := []string{}
	for _, d := range p.Destinations {
		if d.Enabled && cp.PendingMounts[d.ID] && cp.TargetOnline[d.ID] {
			ids = append(ids, d.ID)
		}
	}
	return ids
}

// Keep the full saved plan in the durable queue and compare it before applying
// the server-only execution scope. Scoped runs never carry deletion approval.
func scopedMountPlan(p backup.Plan, r backup.Run) (backup.Plan, error) {
	if r.Trigger != "volume" || !p.Schedule.OnMount || !p.Enabled || r.ApprovedPreviewID != "" || r.DeletesConfirmed {
		return p, errors.New("无效的磁盘连接任务范围")
	}
	selected := map[string]bool{}
	for _, id := range r.DestinationIDs {
		if selected[id] {
			return p, errors.New("重复的磁盘绑定目标")
		}
		selected[id] = true
	}
	targets := []backup.Destination{}
	for _, d := range p.Destinations {
		if selected[d.ID] && d.Enabled {
			targets = append(targets, d)
			delete(selected, d.ID)
		}
	}
	if len(targets) == 0 || len(selected) > 0 {
		return p, errors.New("磁盘绑定目标已失效，请重新预览")
	}
	p.Destinations = targets
	return p, nil
}

// The saved destination UUIDs are the durable disk registry. Merging them into
// discovery retains offline disks and their plan associations across restarts.
func mergeVolumeBindings(out []map[string]any, plans []backup.Plan) []map[string]any {
	byID := map[string]map[string]any{}
	for _, v := range out {
		byID[v["id"].(string)] = v
	}
	for _, p := range plans {
		for _, d := range p.Destinations {
			if d.VolumeID == "" {
				continue
			}
			v := byID[d.VolumeID]
			if v == nil {
				v = map[string]any{"id": d.VolumeID, "name": d.Name, "path": d.MountPath, "capacity": int64(0), "free": int64(0), "connected": false}
				byID[d.VolumeID] = v
				out = append(out, v)
			}
			relative := d.VolumeRelativePath
			if relative != "." && !backup.SafeRelative(relative) {
				if corrected, err := backup.RelativeToVolume(d.MountPath, d.Path); err == nil {
					relative = corrected
				}
			}
			bindings, _ := v["bindings"].([]map[string]any)
			v["bindings"] = append(bindings, map[string]any{"planId": p.ID, "planName": p.Name, "destinationId": d.ID, "relativePath": relative, "enabled": d.Enabled, "automatic": p.Enabled && d.Enabled && p.Schedule.OnMount})
		}
	}
	return out
}
