//go:build integration

package postgres

import (
	"encoding/json"
	"slices"
	"testing"

	"aigo/internal/auth"
	"aigo/internal/domain"

	"github.com/lib/pq"
)

func TestSchema59MigratesOnlyKnownDefaultRoleAndPreservesRevocation(t *testing.T) {
	for _, tc := range []struct {
		name, id    string
		permissions []string
		want        bool
	}{
		{"standard_teacher", domain.RoleTeacher, []string{"question:view", "question:edit", "question:generate", "rule:view", "rule:manage", "rule:use", "batch:run", "question:view_formal", "question:view_eliminated", "question:download", "question:share", "review:submit", "stats:view"}, true},
		{"revoked_on_schema58_before_upgrade", domain.RoleTeacher, []string{"question:view", "question:edit", "question:generate", "rule:view", "rule:manage", "rule:use", "batch:run", "question:view_formal", "question:view_eliminated", "question:download", "question:share", "review:submit", "stats:view"}, false},
		{"customized_teacher", domain.RoleTeacher, []string{domain.PermQuestionView, domain.PermQuestionGenerate}, false},
		{"customized_admin", domain.RoleAdmin, []string{domain.PermQuestionView, domain.PermUserManage}, false},
		{"custom_role", "custom-author", []string{domain.PermQuestionView, domain.PermQuestionGenerate}, false},
		{"expert", domain.RoleExpert, []string{domain.PermQuestionView, domain.PermReviewDo, domain.PermReviewResults}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, dsn, cleanup := migrationTestSchema(t)
			defer cleanup()
			s, err := New(dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			applyMigrationPrefix(t, t.Context(), s, configuredMigrations(baselineSchemaSQL)[:58])
			if _, err = s.db.ExecContext(t.Context(), `INSERT INTO roles(id,name,permissions,is_builtin) VALUES($1,$1,$2,TRUE)`, tc.id, pq.Array(tc.permissions)); err != nil {
				t.Fatal(err)
			}
			// Only the recognized untouched pre-C3 default can receive the
			// one-time new grant. A template edited after Schema58 may represent
			// an explicit confirm revocation with the same old permission set.
			if tc.name == "standard_teacher" {
				if _, err = s.db.ExecContext(t.Context(), `UPDATE roles SET updated_at=(SELECT applied_at-INTERVAL '1 day' FROM schema_migrations WHERE version=58) WHERE id=$1`, tc.id); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = s.Migrate(t.Context()); err != nil {
				t.Fatal(err)
			}
			var permissions []string
			if err = s.db.QueryRowContext(t.Context(), `SELECT permissions FROM roles WHERE id=$1`, tc.id).Scan(pq.Array(&permissions)); err != nil {
				t.Fatal(err)
			}
			if slices.Contains(permissions, domain.PermQuestionConfirm) != tc.want {
				t.Fatalf("unexpected default grant: %v", permissions)
			}
			// Simulate explicit revocation then complete process restart, including the
			// schema runner and built-in seed initializer. No grant may reappear.
			if _, err = s.db.ExecContext(t.Context(), `UPDATE roles SET permissions=array_remove(permissions,'question:confirm') WHERE id=$1`, tc.id); err != nil {
				t.Fatal(err)
			}
			if _, err = s.Migrate(t.Context()); err != nil {
				t.Fatal(err)
			}
			a := auth.NewService(s.DB(), "migration-test", 0)
			if err = a.InitBuiltinRoles(t.Context()); err != nil {
				t.Fatal(err)
			}
			r, err := a.GetRole(t.Context(), tc.id)
			if err != nil {
				t.Fatal(err)
			}
			wantPermissions := append([]string(nil), tc.permissions...)
			// The new retirement migration renames existing private-authoring
			// capabilities without granting confirm or paid learning rights.
			for i, permission := range wantPermissions {
				switch permission {
				case "rule:view":
					wantPermissions[i] = "experience:view"
				case "rule:manage":
					wantPermissions[i] = "experience:manage"
				case "rule:use":
					wantPermissions[i] = "experience:use"
				}
			}
			if tc.name == "standard_teacher" {
				// Schema61 legitimately migrates the untouched C4 template once.
				// The original Schema59 invariant remains: confirm stays revoked.
				wantPermissions = append(wantPermissions, domain.PermLearningAnalyze, domain.PermLearningSave, domain.PermLearningUse)
			}
			// Schema63 is a separately authorized, one-time manual-knowledge grant.
			// It must not restore the revoked question:confirm or add paid learning.
			if tc.id == domain.RoleTeacher || tc.id == domain.RoleExpert || tc.id == "custom-author" {
				extra := []string{domain.PermExperienceView, domain.PermExperienceManage}
				if slices.Contains(wantPermissions, domain.PermQuestionGenerate) {
					extra = append(extra, domain.PermExperienceUse)
				}
				for _, p := range extra {
					if !slices.Contains(wantPermissions, p) {
						wantPermissions = append(wantPermissions, p)
					}
				}
			}
			if tc.id == domain.RoleTeacher || tc.id == domain.RoleExpert || tc.id == "custom-author" {
				wantPermissions = append(wantPermissions, domain.PermKBView, domain.PermKBUse, domain.PermKBSubmit)
			}
			if tc.id == domain.RoleAdmin {
				wantPermissions = append(wantPermissions, domain.PermKBSubmit, domain.PermKBReview)
			}
			if !slices.Equal(r.Permissions, wantPermissions) {
				t.Fatalf("restart rewrote revoked/customized grants: got %v want %v", r.Permissions, wantPermissions)
			}
		})
	}
}

func TestSchema59RepairsOnlyProvenCurrentReviewOutcomes(t *testing.T) {
	_, dsn, cleanup := migrationTestSchema(t)
	defer cleanup()
	s, err := New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	applyMigrationPrefix(t, t.Context(), s, configuredMigrations(baselineSchemaSQL)[:58])
	if _, err = s.db.ExecContext(t.Context(), `INSERT INTO review_flows(id,name,rounds) VALUES('repair','repair','[]')`); err != nil {
		t.Fatal(err)
	}
	type fixture struct {
		name, status, taskStatus, actor    string
		taskVersion                        int
		noPass, pendingCheck, hiddenActive bool
		state, want                        domain.LibraryState
	}
	cases := []fixture{
		{name: "completed", status: "published", taskStatus: "published", taskVersion: 2, actor: "final-reviewer", want: domain.LibraryActive},
		{name: "reject", status: "rejected", taskStatus: "rejected", taskVersion: 2, actor: "reject-reviewer", want: domain.LibraryDeleted},
		{name: "unknown_actor", status: "published", taskStatus: "published", taskVersion: 2, want: domain.LibraryActive},
		{name: "label_only", status: "published", want: domain.LibraryUnfiled},
		{name: "old_version", status: "published", taskStatus: "published", taskVersion: 1, want: domain.LibraryUnfiled},
		{name: "no_pass", status: "published", taskStatus: "published", taskVersion: 2, noPass: true, want: domain.LibraryUnfiled},
		{name: "pending_check", status: "published", taskStatus: "published", taskVersion: 2, pendingCheck: true, want: domain.LibraryUnfiled},
		{name: "hidden_active", status: "published", taskStatus: "published", taskVersion: 2, hiddenActive: true, want: domain.LibraryUnfiled},
		{name: "deleted", status: "published", taskStatus: "published", taskVersion: 2, state: domain.LibraryDeleted, want: domain.LibraryDeleted},
	}
	for _, tc := range cases {
		state := tc.state
		if state == "" {
			state = domain.LibraryUnfiled
		}
		if _, err = s.db.ExecContext(t.Context(), `INSERT INTO questions(id,clinical_stem,options,answer,status,version,library_state) VALUES($1,'历史病例','[]','A',$2,2,$3)`, tc.name, tc.status, string(state)); err != nil {
			t.Fatal(err)
		}
		if !tc.noPass {
			if _, err = s.db.ExecContext(t.Context(), `INSERT INTO ai_review_results(id,question_id,question_version,verdict) VALUES($1,$1,1,'pass')`, tc.name); err != nil {
				t.Fatal(err)
			}
		}
		if tc.taskStatus != "" {
			decision, _ := json.Marshal(map[string]string{"expert_id": tc.actor})
			if _, err = s.db.ExecContext(t.Context(), `INSERT INTO review_tasks(id,question_id,flow_id,status,question_version,final_decision) VALUES($1,$1,'repair',$2,$3,$4::jsonb)`, tc.name, tc.taskStatus, tc.taskVersion, string(decision)); err != nil {
				t.Fatal(err)
			}
		}
		if tc.pendingCheck {
			if _, err = s.db.ExecContext(t.Context(), `INSERT INTO ai_check_tasks(id,question_id,question_version,status) VALUES($1,$1,1,'pending')`, tc.name); err != nil {
				t.Fatal(err)
			}
		}
		if tc.hiddenActive {
			if _, err = s.db.ExecContext(t.Context(), `INSERT INTO review_tasks(id,question_id,flow_id,status,question_version,created_at) VALUES('old-active',$1,'repair','reviewing',1,NOW()-INTERVAL '1 second')`, tc.name); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err = s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		q, e := s.GetQuestion(t.Context(), tc.name)
		if e != nil {
			t.Fatal(e)
		}
		if q.LibraryState != tc.want {
			t.Fatalf("%s: state %s want %s", tc.name, q.LibraryState, tc.want)
		}
		if tc.want == domain.LibraryActive {
			actor := tc.actor
			if actor == "" {
				actor = "legacy_review"
			}
			if q.ConfirmationSource != domain.ConfirmSourceReview || q.ConfirmedVersion != 2 || q.ConfirmedBy != actor || q.ConfirmedAt == nil || q.LibraryRevision != 2 {
				t.Fatalf("%s: incomplete review confirmation: %+v", tc.name, q)
			}
		}
		if tc.name == "reject" && (q.DeletedReason != domain.DeleteReasonReviewReject || q.DeletedBy != tc.actor || q.LibraryRevision != 2) {
			t.Fatalf("missing reject provenance: %+v", q)
		}
		if tc.want == domain.LibraryUnfiled && q.LibraryRevision != 1 {
			t.Fatalf("%s was mutated without proof", tc.name)
		}
	}
	if _, err = s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	q, _ := s.GetQuestion(t.Context(), "completed")
	if q.LibraryRevision != 2 {
		t.Fatal("completed review was remigrated")
	}
}
