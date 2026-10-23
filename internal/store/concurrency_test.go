package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWALReadersRemainAvailableDuringIndexWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "索引 ? #.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	if file, err := os.Stat(path); err != nil || file.Mode().Perm()&0077 != 0 {
		t.Fatal("database URI did not preserve private path", err)
	}
	tx, err := s.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO settings VALUES('uncommitted','{}')"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		t.Fatal("reader blocked behind writer", err)
	}
	defer conn.Close()
	var count, foreignKeys, timeout int
	if err = conn.QueryRowContext(ctx, "SELECT count(*) FROM settings WHERE id='uncommitted'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("reader observed uncommitted write")
	}
	conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys)
	conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&timeout)
	if foreignKeys != 1 || timeout != 30000 {
		t.Fatal("new connection omitted safety pragmas", foreignKeys, timeout)
	}
	tx.Rollback()
	if _, err = conn.ExecContext(ctx, "INSERT INTO run_jobs(run_id,plan_data,state) VALUES('missing','{}','queued')"); err == nil {
		t.Fatal("new connection bypassed foreign key")
	}
}
