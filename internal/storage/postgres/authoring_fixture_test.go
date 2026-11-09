//go:build integration

package postgres

import (
	"testing"
)

func authoringTestStore(t *testing.T) *Store {
	t.Helper()
	_, dsn, cleanup := migrationTestSchema(t)
	t.Cleanup(cleanup)
	s, e := New(dsn)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	if _, e = s.Migrate(t.Context()); e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB().Exec(`INSERT INTO roles(id,name,permissions) VALUES('fixture-authoring','测试身份',ARRAY['experience:view','experience:manage','experience:use','question:generate'])`); e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB().Exec(`INSERT INTO users(id,username,password_hash,display_name,role,roles,enabled) VALUES('owner','owner','unused','所有者','fixture-authoring',ARRAY['fixture-authoring'],TRUE)`); e != nil {
		t.Fatal(e)
	}
	return s
}
