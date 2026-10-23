package store

import (
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"

	"localbackup/internal/backup"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct {
	fingerprintKey   []byte
	fingerprintError string
	DB               *sql.DB
	Path             string
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dsn := url.URL{Scheme: "file", Path: absolutePath}
	query := url.Values{}
	query.Add("_pragma", "busy_timeout(30000)")
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "temp_store(MEMORY)")
	query.Set("_txlock", "immediate")
	dsn.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	for _, q := range []string{"PRAGMA journal_mode=WAL"} {
		if _, err = db.Exec(q); err != nil {
			db.Close()
			return nil, err
		}
	}
	if _, err = db.Exec("CREATE TABLE IF NOT EXISTS schema_migrations(version TEXT PRIMARY KEY)"); err != nil {
		return nil, err
	}
	files, _ := migrations.ReadDir("migrations")
	for _, f := range files {
		var n int
		err = db.QueryRow("SELECT count(*) FROM schema_migrations WHERE version=?", f.Name()).Scan(&n)
		if err != nil {
			return nil, err
		}
		if n > 0 {
			continue
		}
		b, _ := migrations.ReadFile("migrations/" + f.Name())
		tx, e := db.Begin()
		if e != nil {
			return nil, e
		}
		if _, e = tx.Exec(string(b)); e == nil {
			_, e = tx.Exec("INSERT INTO schema_migrations VALUES(?)", f.Name())
		}
		if e != nil {
			tx.Rollback()
			return nil, e
		}
		if e = tx.Commit(); e != nil {
			return nil, e
		}
	}
	os.Chmod(path, 0600)
	absolute, _ := filepath.Abs(path)
	real, e := backup.Canonical(absolute)
	if e == nil {
		absolute = real
	}
	key, keyErr := fingerprintKey(absolute)
	st := &Store{DB: db, Path: absolute, fingerprintKey: key}
	if keyErr != nil {
		st.fingerprintError = "缓存完整性凭据不可用，已退回完整读取: " + keyErr.Error()
	}
	return st, nil
}
func (s *Store) Put(table, id string, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	_, e = s.DB.Exec("INSERT INTO "+table+"(id,data) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data", id, string(b))
	return e
}
func (s *Store) Get(table, id string, v any) error {
	var b string
	if e := s.DB.QueryRow("SELECT data FROM "+table+" WHERE id=?", id).Scan(&b); e != nil {
		return e
	}
	return json.Unmarshal([]byte(b), v)
}
func (s *Store) Delete(table, id string) error {
	_, e := s.DB.Exec("DELETE FROM "+table+" WHERE id=?", id)
	return e
}
func List[T any](s *Store, table string) ([]T, error) {
	r, e := s.DB.Query("SELECT data FROM " + table + " ORDER BY rowid DESC")
	if e != nil {
		return nil, e
	}
	defer r.Close()
	out := []T{}
	for r.Next() {
		var b string
		var v T
		if e = r.Scan(&b); e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(b), &v); e != nil {
			return nil, fmt.Errorf("%s: %w", table, e)
		}
		out = append(out, v)
	}
	return out, r.Err()
}
func (s *Store) Plans() ([]backup.Plan, error) { return List[backup.Plan](s, "backup_plans") }
func (s *Store) Runs() ([]backup.Run, error)   { return List[backup.Run](s, "backup_runs") }
func (s *Store) Snapshots() ([]backup.Snapshot, error) {
	v, e := List[backup.Snapshot](s, "snapshots")
	sort.Slice(v, func(i, j int) bool { return v[i].CreatedAt.After(v[j].CreatedAt) })
	return v, e
}
