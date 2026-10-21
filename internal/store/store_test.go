package store

import (
	"path/filepath"
	"testing"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "sub", "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenWALAndMigrate(t *testing.T) {
	s := open(t)
	var mode string
	if err := s.DB.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("mode=%q err=%v", mode, err)
	}
	v, _ := s.Version()
	if v != 1 {
		t.Fatalf("version %d", v)
	}
	for _, tbl := range []string{"projects", "sessions", "runs", "ui_state", "kv"} {
		var n int
		if err := s.DB.QueryRow("SELECT count(*) FROM " + tbl).Scan(&n); err != nil {
			t.Fatalf("%s: %v", tbl, err)
		}
	}
}

func TestMigrateIdempotentAndReopen(t *testing.T) {
	p := filepath.Join(t.TempDir(), "h.db")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO kv(key,json) VALUES('a','1')`); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var j string
	if err := s.DB.QueryRow(`SELECT json FROM kv WHERE key='a'`).Scan(&j); err != nil || j != "1" {
		t.Fatalf("%q %v", j, err)
	}
}

func TestForeignKeysAndCascade(t *testing.T) {
	s := open(t)
	if _, err := s.DB.Exec(`INSERT INTO sessions(id,project_id,provider,created,updated) VALUES('s','nope','claude',1,1)`); err == nil {
		t.Fatal("FK not enforced")
	}
	mustExec := func(q string) {
		t.Helper()
		if _, err := s.DB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`INSERT INTO projects(id,path,name,setup_cmds) VALUES('p','/x','x','["npm i"]')`)
	mustExec(`INSERT INTO sessions(id,project_id,provider,created,updated) VALUES('s','p','claude',1,1)`)
	mustExec(`INSERT INTO runs(id,session_id,started) VALUES('r','s',1)`)
	mustExec(`DELETE FROM projects WHERE id='p'`)
	var n int
	s.DB.QueryRow(`SELECT count(*) FROM runs`).Scan(&n)
	if n != 0 {
		t.Fatal("cascade failed")
	}
}
