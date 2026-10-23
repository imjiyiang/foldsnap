package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"localbackup/internal/backup"
	"strings"
	"time"
)

func (s *Store) BeginSource(ctx context.Context, source string) (int64, error) {
	var generation int64
	err := s.DB.QueryRowContext(ctx, `INSERT INTO scan_generations(source_id,next_generation) VALUES(?,COALESCE((SELECT generation FROM source_scans WHERE source_id=?),0)+1) ON CONFLICT(source_id) DO UPDATE SET next_generation=MAX(scan_generations.next_generation,excluded.next_generation-1)+1 RETURNING next_generation`, source, source).Scan(&generation)
	return generation, err
}
func (s *Store) SourceHashes(ctx context.Context, source string) (map[string]string, error) {
	if len(s.fingerprintKey) != 32 {
		return map[string]string{}, nil
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT o.object_key,o.content_hash,o.auth_tag FROM source_paths p JOIN filesystem_objects o ON p.object_key=o.object_key WHERE p.source_id=? AND p.state='selected'", source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var key, hash, tag string
		if err = rows.Scan(&key, &hash, &tag); err != nil {
			return nil, err
		}
		if s.validCacheTag(key, hash, tag) {
			out[key] = hash
		}
	}
	return out, rows.Err()
}
func (s *Store) LookupHash(ctx context.Context, key string) (string, error) {
	if len(s.fingerprintKey) != 32 {
		return "", nil
	}
	var hash, tag string
	err := s.DB.QueryRowContext(ctx, "SELECT content_hash,auth_tag FROM filesystem_objects WHERE object_key=?", key).Scan(&hash, &tag)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !s.validCacheTag(key, hash, tag) {
		return "", nil
	}
	return hash, nil
}

type indexedVersion struct{ data, state, reason, key, hash, tag string }

// Read only this bounded batch under the same write transaction as the merge.
// Equality against freshly scanned entries is an optimization, not authority.
func sourceVersions(ctx context.Context, tx *sql.Tx, source string, batch []backup.IndexedPath) (map[string]indexedVersion, error) {
	args := make([]any, 1, len(batch)+1)
	args[0] = source
	for _, record := range batch {
		args = append(args, record.Entry.Path)
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
	rows, err := tx.QueryContext(ctx, `SELECT p.relative_path,p.entry_data,p.state,p.reason,p.object_key,COALESCE(o.content_hash,''),COALESCE(o.auth_tag,'') FROM source_paths p LEFT JOIN filesystem_objects o ON p.object_key=o.object_key WHERE p.source_id=? AND p.relative_path IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]indexedVersion, len(batch))
	for rows.Next() {
		var path string
		var v indexedVersion
		if err := rows.Scan(&path, &v.data, &v.state, &v.reason, &v.key, &v.hash, &v.tag); err != nil {
			return nil, err
		}
		out[path] = v
	}
	return out, rows.Err()
}

func (s *Store) CommitSource(ctx context.Context, scope backup.SourceScope, paths []backup.IndexedPath) error {
	if !backup.ValidID(scope.SourceID) {
		return fmt.Errorf("无效的索引源 ID")
	}
	if err := backup.Checkpoint(ctx); err != nil {
		return err
	}
	if scope.Generation == 0 {
		var err error
		scope.Generation, err = s.BeginSource(ctx, scope.SourceID)
		if err != nil {
			return err
		}
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "CREATE TEMP TABLE IF NOT EXISTS scan_seen(relative_path TEXT PRIMARY KEY)"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM scan_seen"); err != nil {
		return err
	}
	var previousGeneration int64
	err = tx.QueryRowContext(ctx, "SELECT generation FROM source_scans WHERE source_id=?", scope.SourceID).Scan(&previousGeneration)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if scope.Partial && previousGeneration != scope.BaseGeneration {
		return backup.ErrIndexStale
	}
	if previousGeneration >= scope.Generation {
		return nil
	}
	generation := scope.Generation
	object, err := tx.PrepareContext(ctx, `INSERT INTO filesystem_objects VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(object_key) DO UPDATE SET content_hash=excluded.content_hash,verified_at_ns=excluded.verified_at_ns,auth_tag=excluded.auth_tag WHERE filesystem_objects.content_hash<>excluded.content_hash OR filesystem_objects.auth_tag<>excluded.auth_tag OR ?`)
	if err != nil {
		return err
	}
	defer object.Close()
	path, err := tx.PrepareContext(ctx, `INSERT INTO source_paths VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(source_id,relative_path) DO UPDATE SET volume_id=excluded.volume_id,file_id=excluded.file_id,file_type=excluded.file_type,size_bytes=excluded.size_bytes,mtime_ns=excluded.mtime_ns,ctime_ns=excluded.ctime_ns,birthtime_ns=excluded.birthtime_ns,object_key=excluded.object_key,state=excluded.state,reason=excluded.reason,scan_generation=excluded.scan_generation,entry_data=excluded.entry_data WHERE source_paths.entry_data<>excluded.entry_data OR source_paths.state<>excluded.state OR source_paths.reason<>excluded.reason OR source_paths.object_key<>excluded.object_key`)
	if err != nil {
		return err
	}
	defer path.Close()
	// Bound temporary memory and SQL parameters. Unchanged rows need no update,
	// but every path is still marked seen, and every retained hash is authenticated.
	for first := 0; first < len(paths); first += 256 {
		last := first + 256
		if last > len(paths) {
			last = len(paths)
		}
		batch := paths[first:last]
		versions, e := sourceVersions(ctx, tx, scope.SourceID, batch)
		if e != nil {
			return e
		}
		seenArgs := make([]any, len(batch))
		for i, record := range batch {
			if !backup.SafeRelative(record.Entry.Path) || record.Entry.SourceID != scope.SourceID {
				return fmt.Errorf("索引条目越过源范围")
			}
			seenArgs[i] = record.Entry.Path
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO scan_seen(relative_path) VALUES "+strings.TrimSuffix(strings.Repeat("(?),", len(batch)), ","), seenArgs...); err != nil {
			return err
		}
		for _, record := range batch {
			if err = ctx.Err(); err != nil {
				return err
			}
			en := record.Entry
			if !backup.SafeRelative(en.Path) || en.SourceID != scope.SourceID {
				return fmt.Errorf("索引条目越过源范围")
			}
			key := ""
			if len(s.fingerprintKey) == 32 && record.Cacheable && record.State == "selected" && en.Kind == "file" && len(en.Hash) == 64 {
				key = backup.ContentKey(en)
			}
			prior := versions[en.Path]
			if key != "" && (record.Hashed || prior.key != key || prior.hash != en.Hash || !s.validCacheTag(key, prior.hash, prior.tag)) {
				if _, err = object.ExecContext(ctx, key, en.VolumeID, en.FileID, en.Size, en.ModTime, en.ChangeTime, en.BirthTime, en.Hash, scope.ScannedAt.UnixNano(), s.cacheTag(key, en.Hash), record.Hashed); err != nil {
					return err
				}
			}
			data, er := json.Marshal(en)
			if er != nil {
				return er
			}
			if prior.data == string(data) && prior.state == record.State && prior.reason == record.Reason && prior.key == key {
				continue
			}
			if _, err = path.ExecContext(ctx, scope.SourceID, en.Path, en.VolumeID, en.FileID, en.Kind, en.Size, en.ModTime, en.ChangeTime, en.BirthTime, key, record.State, record.Reason, generation, string(data)); err != nil {
				return err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM source_paths WHERE source_id=? AND relative_path NOT IN (SELECT relative_path FROM scan_seen)", scope.SourceID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO source_scans(source_id,generation,root_path,root_identity,volume_id,filesystem,cacheable,policy,started_at_ns,completed_at) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(source_id) DO UPDATE SET generation=excluded.generation,root_path=excluded.root_path,root_identity=excluded.root_identity,volume_id=excluded.volume_id,filesystem=excluded.filesystem,cacheable=excluded.cacheable,policy=excluded.policy,started_at_ns=excluded.started_at_ns,completed_at=excluded.completed_at`, scope.SourceID, generation, scope.Root, scope.RootIdentity, scope.VolumeID, scope.Filesystem, scope.Cacheable, scope.Policy, scope.ScannedAt.UnixNano(), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	mode := "full"
	if scope.Partial {
		mode = "subtree"
	} else {
		scope.FullScanAt = time.Now().UTC()
	}
	if _, err = tx.ExecContext(ctx, "UPDATE source_scans SET scan_mode=?,walked_paths=?,last_full_scan_ns=? WHERE source_id=?", mode, scope.WalkedPaths, scope.FullScanAt.UnixNano(), scope.SourceID); err != nil {
		return err
	}
	tag, err := s.projectionTag(ctx, tx, scope.SourceID)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE source_scans SET projection_tag=? WHERE source_id=?", tag, scope.SourceID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM filesystem_objects WHERE volume_id=? AND verified_at_ns<? AND NOT EXISTS(SELECT 1 FROM source_paths WHERE source_paths.object_key=filesystem_objects.object_key)", scope.VolumeID, scope.ScannedAt.Add(-30*24*time.Hour).UnixNano()); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return backup.Checkpoint(ctx)
}

type IndexSource struct {
	ScanMode       string `json:"scanMode"`
	WalkedPaths    int    `json:"walkedPaths"`
	LastFullScanNS int64  `json:"lastFullScanNs"`
	SourceID       string `json:"sourceId"`
	Root           string `json:"root"`
	VolumeID       string `json:"volumeId"`
	Filesystem     string `json:"filesystem"`
	Cacheable      bool   `json:"cacheable"`
	Generation     int64  `json:"generation"`
	CompletedAt    string `json:"completedAt"`
}
type IndexStatus struct {
	CacheAvailable bool          `json:"cacheAvailable"`
	CacheError     string        `json:"cacheError,omitempty"`
	Paths          int64         `json:"paths"`
	Objects        int64         `json:"objects"`
	Selected       int64         `json:"selected"`
	Excluded       int64         `json:"excluded"`
	Sources        []IndexSource `json:"sources"`
}

func (s *Store) IndexStatus(ctx context.Context) (IndexStatus, error) {
	out := IndexStatus{Sources: []IndexSource{}, CacheAvailable: len(s.fingerprintKey) == 32, CacheError: s.fingerprintError}
	if err := s.DB.QueryRowContext(ctx, "SELECT count(*),COALESCE(sum(state='selected'),0),COALESCE(sum(state='excluded'),0) FROM source_paths").Scan(&out.Paths, &out.Selected, &out.Excluded); err != nil {
		return out, err
	}
	if err := s.DB.QueryRowContext(ctx, "SELECT count(*) FROM filesystem_objects").Scan(&out.Objects); err != nil {
		return out, err
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT source_id,root_path,volume_id,filesystem,cacheable,generation,completed_at,scan_mode,walked_paths,last_full_scan_ns FROM source_scans ORDER BY completed_at DESC")
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var src IndexSource
		if err = rows.Scan(&src.SourceID, &src.Root, &src.VolumeID, &src.Filesystem, &src.Cacheable, &src.Generation, &src.CompletedAt, &src.ScanMode, &src.WalkedPaths, &src.LastFullScanNS); err != nil {
			return out, err
		}
		out.Sources = append(out.Sources, src)
	}
	return out, rows.Err()
}
