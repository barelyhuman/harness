package store

import (
	"testing"
)

func TestAllTablesWritable(t *testing.T) {
	s := open(t)
	must := func(q string, args ...any) {
		t.Helper()
		if _, err := s.DB.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	must(`INSERT INTO projects(id,path,name,setup_cmds,agent_roles,last_opened) VALUES('p','/r','r','[]','{}',1)`)
	must(`INSERT INTO sessions(id,project_id,provider,created,updated) VALUES('s','p','fake',1,2)`)
	must(`INSERT INTO runs(id,session_id,started,status) VALUES('r','s',1,'running')`)
	must(`INSERT INTO ui_state(scope_key,json) VALUES('k','{}')`)
	must(`UPDATE projects SET agent_roles='{"planner":"x"}' WHERE id='p'`)
	var roles string
	if err := s.DB.QueryRow(`SELECT agent_roles FROM projects WHERE id='p'`).Scan(&roles); err != nil || roles == "" {
		t.Fatalf("%q %v", roles, err)
	}
}

func TestVersionZeroMigratesUp(t *testing.T) {
	p := filepathJoin(t, "fresh.db")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := s.Version()
	s.Close()
	if v != 2 {
		t.Fatalf("version %d", v)
	}
}

func filepathJoin(t *testing.T, name string) string {
	t.Helper()
	return t.TempDir() + "/" + name
}
