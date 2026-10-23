package store

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
)

func (s *Store) watchTag(ctx context.Context, tx *sql.Tx) (string, error) {
	if len(s.fingerprintKey) != 32 {
		return "", nil
	}
	mac := hmac.New(sha256.New, s.fingerprintKey)
	mac.Write([]byte("watch-state-v1\x00"))
	encode := json.NewEncoder(mac)
	var cursor string
	err := tx.QueryRowContext(ctx, "SELECT data FROM watch_cursors WHERE id='host'").Scan(&cursor)
	if err != nil && err != sql.ErrNoRows {
		return "", err
	}
	encode.Encode(cursor)
	rows, err := tx.QueryContext(ctx, "SELECT id,plan_id,source_id,relative_path,event_id,flags,requires_scan,changed_at_ns FROM change_events ORDER BY id")
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var id, flags, scan, at int64
		var plan, source, path, event string
		if err = rows.Scan(&id, &plan, &source, &path, &event, &flags, &scan, &at); err != nil {
			return "", err
		}
		if err = encode.Encode([]any{id, plan, source, path, event, flags, scan, at}); err != nil {
			return "", err
		}
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(mac.Sum(nil)), nil
}
func (s *Store) VerifyWatchState(ctx context.Context, tx *sql.Tx) (bool, error) {
	if len(s.fingerprintKey) != 32 {
		return false, nil
	}
	var tag string
	err := tx.QueryRowContext(ctx, "SELECT auth_tag FROM watch_integrity WHERE id='host'").Scan(&tag)
	if err == sql.ErrNoRows {
		var count int
		if err = tx.QueryRowContext(ctx, "SELECT (SELECT COUNT(*) FROM change_events)+(SELECT COUNT(*) FROM watch_cursors)").Scan(&count); err != nil {
			return false, err
		}
		return count == 0, nil
	}
	if err != nil {
		return false, err
	}
	expected, err := s.watchTag(ctx, tx)
	return tag != "" && hmac.Equal([]byte(expected), []byte(tag)), err
}
func (s *Store) SealWatchState(ctx context.Context, tx *sql.Tx) error {
	tag, err := s.watchTag(ctx, tx)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO watch_integrity VALUES('host',?) ON CONFLICT(id) DO UPDATE SET auth_tag=excluded.auth_tag", tag)
	return err
}
