package store

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"localbackup/internal/backup"
	"time"
)

func (s *Store) projectionTag(ctx context.Context, tx *sql.Tx, id string) (string, error) {
	if len(s.fingerprintKey) != 32 {
		return "", nil
	}
	var generation, fullAt int64
	var root, identity, volume, filesystem, policy string
	var cacheable bool
	if err := tx.QueryRowContext(ctx, "SELECT generation,root_path,root_identity,volume_id,filesystem,cacheable,policy,last_full_scan_ns FROM source_scans WHERE source_id=?", id).Scan(&generation, &root, &identity, &volume, &filesystem, &cacheable, &policy, &fullAt); err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, s.fingerprintKey)
	mac.Write([]byte("source-projection-v1\x00"))
	encode := json.NewEncoder(mac)
	if err := encode.Encode([]any{id, generation, root, identity, volume, filesystem, cacheable, policy, fullAt}); err != nil {
		return "", err
	}
	rows, err := tx.QueryContext(ctx, "SELECT relative_path,state,reason,entry_data FROM source_paths WHERE source_id=? ORDER BY relative_path", id)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var path, state, reason, data string
		if err = rows.Scan(&path, &state, &reason, &data); err != nil {
			return "", err
		}
		if err = encode.Encode([]string{path, state, reason, data}); err != nil {
			return "", err
		}
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(mac.Sum(nil)), nil
}
func (s *Store) LoadSourceProjection(ctx context.Context, id string) (*backup.SourceProjection, error) {
	if len(s.fingerprintKey) != 32 {
		return nil, nil
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var tag string
	p := &backup.SourceProjection{Paths: []backup.IndexedPath{}}
	p.Scope.SourceID = id
	var fullAt int64
	err = tx.QueryRowContext(ctx, "SELECT generation,root_path,root_identity,volume_id,filesystem,cacheable,policy,last_full_scan_ns,projection_tag FROM source_scans WHERE source_id=?", id).Scan(&p.Scope.Generation, &p.Scope.Root, &p.Scope.RootIdentity, &p.Scope.VolumeID, &p.Scope.Filesystem, &p.Scope.Cacheable, &p.Scope.Policy, &fullAt, &tag)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	expected, err := s.projectionTag(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if tag == "" || !hmac.Equal([]byte(expected), []byte(tag)) {
		return nil, nil
	}
	p.Scope.FullScanAt = time.Unix(0, fullAt)
	rows, err := tx.QueryContext(ctx, "SELECT relative_path,state,reason,entry_data FROM source_paths WHERE source_id=? ORDER BY relative_path", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var path, data string
		record := backup.IndexedPath{}
		if err = rows.Scan(&path, &record.State, &record.Reason, &data); err != nil {
			return nil, err
		}
		if json.Unmarshal([]byte(data), &record.Entry) != nil || record.Entry.Path != path || record.Entry.SourceID != id || !backup.SafeRelative(path) || (record.State != "selected" && record.State != "excluded") {
			return nil, nil
		}
		p.Paths = append(p.Paths, record)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return p, nil
}
