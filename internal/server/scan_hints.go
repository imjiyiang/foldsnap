package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"localbackup/internal/backup"
)

// Hints are runtime-only capabilities, never accepted from a JSON plan. Both
// the complete pending event set and the native root coverage must be sealed.
func (s *Server) withDirtyHints(ctx context.Context, p backup.Plan, force bool) (backup.Plan, error) {
	p.RuntimeDirtyRoots = nil
	if force || !p.Enabled || !p.Schedule.OnChange || !s.nativeHealthy() {
		return p, nil
	}
	tx, err := s.Store.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return p, err
	}
	defer tx.Rollback()
	valid, err := s.Store.VerifyWatchState(ctx, tx)
	if err != nil || !valid {
		return p, err
	}
	var data string
	err = tx.QueryRowContext(ctx, "SELECT data FROM watch_cursors WHERE id='host'").Scan(&data)
	if err == sql.ErrNoRows {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	var cursor watchCursor
	if json.Unmarshal([]byte(data), &cursor) != nil {
		return p, nil
	}
	roots := map[string]bool{}
	for _, root := range cursor.Roots {
		roots[root] = true
	}
	hints := map[string][]string{}
	for _, src := range p.Sources {
		if src.Enabled && roots[src.Path] {
			hints[src.ID] = []string{}
		}
	}
	rows, err := tx.QueryContext(ctx, "SELECT source_id,relative_path,requires_scan FROM change_events WHERE plan_id=? ORDER BY id", p.ID)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, path string
		var full bool
		if err = rows.Scan(&id, &path, &full); err != nil {
			return p, err
		}
		paths, exists := hints[id]
		if !exists || len(paths) == 1 && paths[0] == "." {
			continue
		}
		if full || !backup.SafeRelative(path) || len(paths) >= 1024 {
			hints[id] = []string{"."}
		} else {
			hints[id] = append(paths, path)
		}
	}
	if err = rows.Err(); err != nil {
		return p, err
	}
	p.RuntimeDirtyRoots = hints
	return p, nil
}
