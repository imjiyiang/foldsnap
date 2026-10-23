package store

import (
	"localbackup/internal/backup"
	"path/filepath"
	"testing"
)

func TestMigrationsAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Put("backup_plans", "test", backup.Plan{ID: "test", Name: "Persisted"}); e != nil {
		t.Fatal(e)
	}
	s.DB.Close()
	s, e = Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	var p backup.Plan
	if e = s.Get("backup_plans", "test", &p); e != nil || p.Name != "Persisted" {
		t.Fatalf("lost state: %v", e)
	}
	var journal string
	s.DB.QueryRow("PRAGMA journal_mode").Scan(&journal)
	if journal != "wal" {
		t.Fatal("WAL not enabled")
	}
	var n int
	s.DB.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&n)
	files, err := migrations.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	if n != len(files) {
		t.Fatal("migration duplicated")
	}
}
