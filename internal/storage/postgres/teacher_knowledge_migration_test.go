//go:build integration

package postgres

import (
	"slices"
	"testing"

	"github.com/lib/pq"
)

func TestTeacherKnowledgeMigrationGrantsTemplatesOnce(t *testing.T) {
	_, dsn, cleanup := migrationTestSchema(t)
	t.Cleanup(cleanup)
	s, err := New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	applyMigrationPrefix(t, t.Context(), s, configuredMigrations(baselineSchemaSQL)[:62])
	_, err = s.db.Exec(`INSERT INTO roles(id,name,permissions,is_builtin) VALUES
 ('teacher','命题教师',ARRAY['question:view','question:generate'],TRUE),
 ('expert','审题老师',ARRAY['question:view','review:do'],TRUE),
 ('custom-author','内科命题',ARRAY['batch:run'],FALSE),
 ('custom-reviewer','内科审核',ARRAY['review:do'],FALSE),
 ('reader','只读',ARRAY['question:view'],FALSE),
 ('empty','无权限',ARRAY[]::text[],FALSE),
 ('admin','已撤权管理员',ARRAY['user:manage'],TRUE)`)
	if err != nil {
		t.Fatal(err)
	}
	var oldChecksums string
	if err = s.db.QueryRow(`SELECT string_agg(checksum,',' ORDER BY version) FROM schema_migrations`).Scan(&oldChecksums); err != nil {
		t.Fatal(err)
	}
	applied, err := s.Migrate(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != int(LatestSchemaVersion-62) || applied[0].Version != 63 {
		t.Fatalf("expected forward upgrade through teacher grant migration, got %+v", applied)
	}
	for _, id := range []string{"teacher", "expert", "custom-author", "custom-reviewer", "reader", "empty", "admin"} {
		var got []string
		if err = s.db.QueryRow(`SELECT permissions FROM roles WHERE id=$1`, id).Scan(pq.Array(&got)); err != nil {
			t.Fatal(err)
		}
		teacher := slices.Contains([]string{"teacher", "expert", "custom-author", "custom-reviewer"}, id)
		for _, p := range []string{"experience:view", "experience:manage"} {
			if slices.Contains(got, p) != teacher {
				t.Fatalf("%s: wrong %s grant: %v", id, p, got)
			}
		}
		author := id == "teacher" || id == "custom-author"
		if slices.Contains(got, "experience:use") != author {
			t.Fatalf("%s: wrong authoring grant: %v", id, got)
		}
		for _, p := range []string{"kb:view", "kb:use", "kb:submit"} {
			want := teacher || (id == "admin" && p == "kb:submit")
			if slices.Contains(got, p) != want {
				t.Fatal("Schema65 explicit grant", id, p, got)
			}
		}
		if slices.Contains(got, "kb:review") != (id == "admin") {
			t.Fatal("review default", id, got)
		}
		for _, p := range []string{"kb:manage", "learning:analyze", "learning:save", "learning:use"} {
			if slices.Contains(got, p) {
				t.Fatalf("%s: unrelated privilege %s granted", id, p)
			}
		}
	}
	var currentChecksums string
	if err = s.db.QueryRow(`SELECT string_agg(checksum,',' ORDER BY version) FROM schema_migrations WHERE version<=62`).Scan(&currentChecksums); err != nil || currentChecksums != oldChecksums {
		t.Fatal("historical checksums changed", err)
	}
	if _, err = s.db.Exec(`UPDATE roles SET permissions=array_remove(array_remove(permissions,'experience:view'),'experience:manage') WHERE id='expert'`); err != nil {
		t.Fatal(err)
	}
	if applied, err = s.Migrate(t.Context()); err != nil || len(applied) != 0 {
		t.Fatal("migration repeated", applied, err)
	}
	var grants bool
	if err = s.db.QueryRow(`SELECT permissions && ARRAY['experience:view','experience:manage'] FROM roles WHERE id='expert'`).Scan(&grants); err != nil || grants {
		t.Fatal("revoked permission restored", err)
	}
}
