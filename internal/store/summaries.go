package store

import (
	"context"
	"encoding/json"
	"localbackup/internal/backup"
	"sort"
)

// List summaries without materializing file manifests in Go. Full records remain
// available to execution, restore and integrity checks through Get/Snapshots.
func (s *Store) SnapshotSummaries(ctx context.Context, planID string) ([]backup.Snapshot, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT json_remove(data,'$.entries') FROM snapshots WHERE ?='' OR json_extract(data,'$.planId')=?`, planID, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []backup.Snapshot{}
	for rows.Next() {
		var raw string
		var v backup.Snapshot
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, rows.Err()
}
func (s *Store) RunSummaries(ctx context.Context) ([]backup.Run, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT json_remove(data,'$.preview.operations','$.preview.removals') FROM backup_runs ORDER BY rowid DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []backup.Run{}
	for rows.Next() {
		var raw string
		var v backup.Run
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) LatestSnapshot(ctx context.Context, planID, destinationID string) (*backup.Snapshot, error) {
	summaries, err := s.SnapshotSummaries(ctx, planID)
	if err != nil {
		return nil, err
	}
	for _, summary := range summaries {
		if summary.DestinationID == destinationID {
			var snapshot backup.Snapshot
			if err = s.Get("snapshots", summary.ID, &snapshot); err != nil {
				return nil, err
			}
			return &snapshot, nil
		}
	}
	return nil, nil
}
