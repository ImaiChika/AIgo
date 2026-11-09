//go:build integration

package api

import (
	"encoding/json"
	"strings"
	"testing"

	"aigo/internal/domain"
	"aigo/internal/knowledge"
)

func TestOutlinePreviewBindsFilesModeVersionIdentityAndCurrentState(t *testing.T) {
	for _, name := range []string{"valid", "changed_file", "changed_mode", "changed_version", "changed_state", "tampered_token", "other_user", "revoked_permission", "deleted_version"} {
		t.Run(name, func(t *testing.T) {
			s, store, cleanup := authHandlerTestServer(t)
			defer cleanup()
			s.kpSvc = knowledge.NewService(store)
			u, err := s.authSvc.CreateUser("outline-manager", "outline-test-password", "本轮大纲管理", "", []string{domain.PermKnowledgeMng}, nil)
			if err != nil {
				t.Fatal(err)
			}
			h := s.Handler()
			token := loginForAuthTest(t, h, u.Username, "outline-test-password", "198.51.100.88")
			v, err := s.kpSvc.CreateVersion(t.Context(), "预览目标", 2026, "")
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.kpSvc.CreatePoint(t.Context(), domain.KnowledgePoint{VersionID: v.ID, Category: "临床综合", Subject: "呼吸系统", Topic: "原有考点", OutlineCode: "old"})
			if err != nil {
				t.Fatal(err)
			}
			csv := "分类,专业/系统,单元,细目,要点,大纲代码\n临床综合,呼吸系统,感染,肺炎,肺炎诊断,new\n基础医学,生理,细胞,膜电位,静息电位,basic\n"
			fields := map[string]string{"version_id": v.ID, "mode": "replace", "preview": "1"}
			res := uploadOutlineFile(t, h, token, fields, "大纲.csv", csv)
			if res.Code != 200 {
				t.Fatal(res.Code, res.Body)
			}
			var preview knowledge.ImportPreview
			if json.Unmarshal(res.Body.Bytes(), &preview) != nil || preview.PreviewToken == "" || preview.ReplaceWouldRemove != 1 {
				t.Fatal(res.Body)
			}
			delete(fields, "preview")
			fields["preview_token"] = preview.PreviewToken
			want := 409
			switch name {
			case "valid":
				want = 200
			case "changed_file":
				csv = strings.Replace(csv, "肺炎诊断", "肺炎治疗", 1)
			case "changed_mode":
				fields["mode"] = "merge"
			case "changed_version":
				other, e := s.kpSvc.CreateVersion(t.Context(), "其他大纲", 2025, "")
				if e != nil {
					t.Fatal(e)
				}
				fields["version_id"] = other.ID
			case "changed_state":
				_, err = s.kpSvc.CreatePoint(t.Context(), domain.KnowledgePoint{VersionID: v.ID, Category: "临床综合", Subject: "呼吸系统", Topic: "另一窗口新题点", OutlineCode: "concurrent"})
				if err != nil {
					t.Fatal(err)
				}
			case "tampered_token":
				fields["preview_token"] = "x" + preview.PreviewToken
			case "other_user":
				_, err = s.authSvc.CreateUser("other-manager", "outline-test-password", "另一个管理员", "", []string{domain.PermKnowledgeMng}, nil)
				if err != nil {
					t.Fatal(err)
				}
				token = loginForAuthTest(t, h, "other-manager", "outline-test-password", "198.51.100.89")
			case "revoked_permission":
				_, err = s.authSvc.UpdateUser(u.ID, u.DisplayName, "", nil, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				want = 403
			case "deleted_version":
				if _, err = store.DeleteKnowledgeVersion(t.Context(), v.ID); err != nil {
					t.Fatal(err)
				}
				want = 404
			}
			res = uploadOutlineFile(t, h, token, fields, "大纲.csv", csv)
			if res.Code != want {
				t.Fatalf("confirmation got %d want %d: %s", res.Code, want, res.Body)
			}
			if name == "deleted_version" {
				return
			}
			points, err := store.ListVersionPoints(t.Context(), v.ID)
			if err != nil {
				t.Fatal(err)
			}
			if name == "valid" {
				if len(points) != 1 || points[0].OutlineCode != "new" {
					t.Fatalf("wrong confirmed content: %+v", points)
				}
				return
			}
			for _, p := range points {
				if p.OutlineCode == "new" {
					t.Fatal("rejected import still wrote content")
				}
			}
			wantCount := 1
			if name == "changed_state" {
				wantCount = 2
			}
			if len(points) != wantCount {
				t.Fatalf("rejected replace removed unseen points: %+v", points)
			}
		})
	}
}
