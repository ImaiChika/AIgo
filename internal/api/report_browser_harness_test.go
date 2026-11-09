//go:build integration

package api

import (
	"aigo/internal/domain"
	"encoding/json"
	"net"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestReportBrowserHarness(t *testing.T) {
	if os.Getenv("AIGO_RUN_REPORT_BROWSER") != "1" {
		t.Skip("explicit local browser fixture only")
	}
	s, h, token, v, cleanup := libraryTestEnv(t)
	defer cleanup()
	t.Chdir(t.TempDir())
	ids := map[string]string{}
	for _, item := range []struct {
		id, topic, profession string
		published             bool
	}{{"report-ui-a", "筛选甲", "内科", false}, {"report-ui-b", "筛选乙", "外科", false}, {"report-ui-c", "其他丙", "内科", false}, {"report-ui-published", "个人审核入库", "内科", true}, {"report-ui-global", "共享范围", "内科", true}} {
		q := seedPassedQuestion(t, s, h, token, v, item.id, "患者，男，45岁。"+item.topic+"，最可能的诊断是")
		current := mustLibraryQuestion(t, s, q.ID)
		current.Profession = item.profession
		current.ClinicalStem = "男，45岁。" + item.topic + "表现持续2天。该患者最可能的诊断是"
		current.OutlineCode = "lib.001"
		current.Explanation = "正确答案为A。测试病例支持该诊断。B项不符合；C项不符合；D项不符合；E项不符合。故选A。"
		current.Version++
		if item.published {
			current.Status = domain.StatusPublished
		}
		if err := s.questionStore.SaveQuestion(t.Context(), *current); err != nil {
			t.Fatal(err)
		}
		if !item.published {
			r := confirmViaAPI(t, h, token, libraryItemsPayload(t, s, q.ID))
			if r.Code != 200 {
				t.Fatal(r.Code, r.Body)
			}
		}
		ids[item.id] = q.ID
	}
	if err := s.shareStore.CreateQuestionShare(t.Context(), domain.QuestionShareRequest{ID: "report-ui-share", QuestionID: ids["report-ui-global"], OwnerID: mustLibraryQuestion(t, s, ids["report-ui-global"]).OwnerID, Status: domain.QuestionSharePending, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.shareStore.ReviewQuestionShare(t.Context(), "report-ui-share", "fixture-reviewer", domain.QuestionShareApproved, ""); err != nil {
		t.Fatal(err)
	}
	admin := loginForAuthTest(t, h, "admin", "admin-password", "198.51.100.220")
	created := serveGenerateJSON(t, h, "POST", "/api/users", admin, map[string]any{"username": "report-global", "password": "report-global-password", "permissions": []string{domain.PermQuestionViewGlobal, domain.PermQuestionViewFormal, domain.PermQuestionDownload}})
	if created.Code != 201 {
		t.Fatal(created.Code, created.Body)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:15602")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(h)
	server.Listener.Close()
	server.Listener = listener
	server.Start()
	defer server.Close()
	root := os.Getenv("AIGO_REPORT_TMP")
	raw, _ := json.Marshal(ids)
	if root == "" || os.WriteFile(root+"/browser-marker.json", raw, 0600) != nil {
		t.Fatal("missing owned browser marker")
	}
	for {
		if _, err := os.Stat(root + "/browser-stop"); err == nil {
			return
		}
		select {
		case <-t.Context().Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}
