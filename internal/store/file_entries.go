package store

import (
	"context"
	"encoding/json"
	"fmt"

	"localbackup/internal/backup"
)

type FileEntryChanges struct {
	Added, Updated, Deleted, Unchanged int
}

// SyncFileEntries replaces only the enabled source scopes, atomically. Identical
// records cause no SQL mutations, including after a verified unchanged backup.
// This catalog is separate from the authenticated scan fingerprint index.
func (s *Store) SyncFileEntries(ctx context.Context, sources []backup.Source, entries []backup.Entry) (FileEntryChanges, error) {
	var changes FileEntryChanges
	scopes := map[string]map[string]string{}
	for _, src := range sources {
		if src.Enabled {
			if !backup.ValidID(src.ID) {
				return changes, fmt.Errorf("目录索引源 ID 无效")
			}
			scopes[src.ID] = map[string]string{}
		}
	}
	for _, en := range entries {
		paths, ok := scopes[en.SourceID]
		if !ok || !backup.SafeRelative(en.Path) {
			return changes, fmt.Errorf("目录索引路径超出本次源范围")
		}
		if _, exists := paths[en.Path]; exists {
			return changes, fmt.Errorf("目录索引路径重复")
		}
		data, err := json.Marshal(en)
		if err != nil {
			return changes, err
		}
		paths[en.Path] = string(data)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return changes, err
	}
	defer tx.Rollback()
	for sourceID, paths := range scopes {
		rows, err := tx.QueryContext(ctx, "SELECT relative_path,data FROM file_entries WHERE source_id=?", sourceID)
		if err != nil {
			return changes, err
		}
		old := map[string]string{}
		for rows.Next() {
			var path, data string
			if err = rows.Scan(&path, &data); err != nil {
				rows.Close()
				return changes, err
			}
			old[path] = data
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return changes, err
		}
		for path, data := range paths {
			if err = ctx.Err(); err != nil {
				return changes, err
			}
			prior, exists := old[path]
			if exists && prior == data {
				changes.Unchanged++
			} else {
				if _, err = tx.ExecContext(ctx, "INSERT INTO file_entries(source_id,relative_path,data) VALUES(?,?,?) ON CONFLICT(source_id,relative_path) DO UPDATE SET data=excluded.data", sourceID, path, data); err != nil {
					return changes, err
				}
				if exists {
					changes.Updated++
				} else {
					changes.Added++
				}
			}
			delete(old, path)
		}
		for path := range old {
			if _, err = tx.ExecContext(ctx, "DELETE FROM file_entries WHERE source_id=? AND relative_path=?", sourceID, path); err != nil {
				return changes, err
			}
			changes.Deleted++
		}
	}
	return changes, tx.Commit()
}
