//go:build integration

package api

import (
	"aigo/internal/domain"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestKnowledgeSummaryContractAndPayload(t *testing.T) {
	_, _, h, admin, point, _, _, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	create := func(name string, permissions []string) string {
		r := serveGenerateJSON(t, h, "POST", "/api/users", admin, map[string]any{"username": name, "password": "summary-fixture-password", "permissions": permissions})
		if r.Code != 201 {
			t.Fatal(r.Code, r.Body)
		}
		return loginForAuthTest(t, h, name, "summary-fixture-password", "198.51.100.211")
	}
	owner := create("summary-owner", []string{domain.PermExperienceView, domain.PermExperienceManage, domain.PermExperienceUse, domain.PermQuestionGenerate, domain.PermKBView, domain.PermKBUse})
	peer := create("summary-peer", []string{domain.PermExperienceView, domain.PermExperienceManage, domain.PermKBView})
	reviewer := create("summary-reviewer", []string{domain.PermKBView, domain.PermKBReview})
	call := func(method, path, token string, input any, want int) []byte {
		t.Helper()
		r := serveGenerateJSON(t, h, method, path, token, input)
		if r.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, r.Code, r.Body)
		}
		return r.Body.Bytes()
	}
	var first domain.PrivateKnowledgeCard
	for i := 0; i < 20; i++ {
		card := domain.KnowledgeCard{Title: fmt.Sprintf("summary-%02d %%_", i), Kind: "custom", Status: "active", KnowledgePoints: []domain.KnowledgePoint{point}, Source: strings.Repeat("源", 1200) + "HIDDEN-SOURCE-END", Note: "PRIVATE-NOTE-SECRET" + strings.Repeat("注", 1200), Content: domain.KnowledgeContent{CustomText: strings.Repeat("文", 500) + "HIDDEN-BODY-SECRET" + strings.Repeat("文", 5000), ExamTargets: []string{strings.Repeat("考", 200)}, Features: []string{strings.Repeat("临床", 300)}}}
		if i%2 == 1 {
			card.Status = "draft"
		}
		for j := 0; j < 3; j++ {
			card.Content.Facts = append(card.Content.Facts, domain.KnowledgeFact{ID: fmt.Sprint("f", j), Text: strings.Repeat("实", 800), Source: strings.Repeat("s", 200), AppliesWhen: strings.Repeat("条", 100)})
		}
		var saved struct {
			Cards []domain.PrivateKnowledgeCard `json:"cards"`
		}
		json.Unmarshal(call("POST", "/api/private-knowledge", owner, domain.KnowledgeCardSaveRequest{Key: fmt.Sprint("summary-create-", i), Cards: []domain.KnowledgeCard{card}}, 200), &saved)
		if i == 0 {
			first = saved.Cards[0]
			call("PUT", "/api/knowledge-selections", owner, map[string]any{"scope": "private", "id": first.ID, "selected": true}, 200)
		}
		call("POST", "/api/knowledge-base", admin, card, 200)
	}
	call("POST", "/api/knowledge-base", admin, domain.KnowledgeCard{Title: "summary-global", Kind: "authoring_guideline", Content: domain.KnowledgeContent{CustomText: "全局方法"}, Source: "来源"}, 200)
	for _, tc := range []struct{ name, path, token string }{{"private", "/api/private-knowledge", owner}, {"shared", "/api/knowledge-base", admin}} {
		t.Run(tc.name, func(t *testing.T) {
			for _, filter := range []string{"q=summary-&limit=30", "q=%25_&limit=3&offset=2", "q=summary-&status=draft&limit=5", "kind=medical&limit=30", "q=不存在&limit=5"} {
				full := call("GET", tc.path+"?"+filter, tc.token, nil, 200)
				light := call("GET", tc.path+"?view=summary&"+filter, tc.token, nil, 200)
				var prior struct {
					Cards []domain.PrivateKnowledgeCard `json:"cards"`
					Total int                           `json:"total"`
				}
				var now struct {
					View  string                        `json:"view"`
					Items []domain.KnowledgeCardSummary `json:"items"`
					Total int                           `json:"total"`
				}
				if e := json.Unmarshal(full, &prior); e != nil {
					t.Fatal(e)
				}
				if e := json.Unmarshal(light, &now); e != nil {
					t.Fatal(e)
				}
				if now.View != "summary" || now.Total != prior.Total || len(now.Items) != len(prior.Cards) {
					t.Fatal("projection changed pagination/filters/count")
				}
				for i, c := range prior.Cards {
					var selected *bool
					if tc.name == "private" {
						selected = &c.Selected
					}
					expected := domain.SummarizeKnowledgeCard(c.KnowledgeCard, selected)
					want, _ := json.Marshal(expected)
					got, _ := json.Marshal(now.Items[i])
					if string(want) != string(got) {
						t.Fatalf("metadata/order mismatch: %s / %s", want, got)
					}
				}
				if bytes.Contains(light, []byte(`"cards"`)) || bytes.Contains(light, []byte(`"content"`)) || bytes.Contains(light, []byte(`"note"`)) || bytes.Contains(light, []byte(`"origin"`)) || bytes.Contains(light, []byte("PRIVATE-NOTE-SECRET")) || bytes.Contains(light, []byte("HIDDEN-BODY-SECRET")) || bytes.Contains(light, []byte("HIDDEN-SOURCE-END")) {
					t.Fatal("summary contains full/private material")
				}
			}
			full := call("GET", tc.path+"?q=summary-&limit=30", tc.token, nil, 200)
			light := call("GET", tc.path+"?view=summary&q=summary-&limit=30", tc.token, nil, 200)
			gzipSize := func(data []byte) int {
				var out bytes.Buffer
				w := gzip.NewWriter(&out)
				w.Write(data)
				w.Close()
				return out.Len()
			}
			if len(light) >= len(full)/4 {
				t.Fatal("summary payload did not meaningfully shrink")
			}
			for _, view := range []string{"full", "summary"} {
				start := time.Now()
				for i := 0; i < 10; i++ {
					suffix := ""
					if view == "summary" {
						suffix = "&view=summary"
					}
					call("GET", tc.path+"?q=summary-&limit=30"+suffix, tc.token, nil, 200)
				}
				t.Logf("knowledge_list mode=%s view=%s iterations=10 elapsed=%v", tc.name, view, time.Since(start))
			}
			t.Logf("knowledge_payload mode=%s full_json=%d summary_json=%d full_gzip=%d summary_gzip=%d", tc.name, len(full), len(light), gzipSize(full), gzipSize(light))
		})
	}
	// Explicit summary markers are rejected by both save handlers, even as drafts.
	for _, tc := range []struct{ path, token, method string }{{"/api/private-knowledge", owner, "POST"}, {"/api/knowledge-base", admin, "PUT"}} {
		var list struct {
			Items []map[string]any `json:"items"`
		}
		json.Unmarshal(call("GET", tc.path+"?view=summary&q=summary-&limit=30", tc.token, nil, 200), &list)
		item := list.Items[0]
		item["status"] = "draft"
		if tc.method == "POST" {
			call("POST", tc.path, tc.token, map[string]any{"key": "reject-summary", "cards": []any{item}}, 400)
		} else {
			call("PUT", tc.path+"/"+item["id"].(string), tc.token, item, 400)
		}
	}
	// The new body endpoint stays owner scoped and does not expose a false selection state.
	data := call("GET", "/api/private-knowledge/"+first.ID, owner, nil, 200)
	if !bytes.Contains(data, []byte("HIDDEN-BODY-SECRET")) || !bytes.Contains(data, []byte("PRIVATE-NOTE-SECRET")) || bytes.Contains(data, []byte(`"selected"`)) {
		t.Fatal("detail body contract changed")
	}
	var editable domain.KnowledgeCard
	if e := json.Unmarshal(data, &editable); e != nil {
		t.Fatal(e)
	}
	editable.Status = "draft"
	var changed struct {
		Cards []domain.PrivateKnowledgeCard `json:"cards"`
	}
	json.Unmarshal(call("POST", "/api/private-knowledge", owner, domain.KnowledgeCardSaveRequest{Key: "roundtrip-state", Cards: []domain.KnowledgeCard{editable}}, 200), &changed)
	if len(changed.Cards) != 1 || changed.Cards[0].Note != editable.Note || changed.Cards[0].Source != editable.Source || changed.Cards[0].Content.CustomText != editable.Content.CustomText {
		t.Fatal("complete detail roundtrip lost body/source/note")
	}
	call("GET", "/api/private-knowledge/"+first.ID, peer, nil, 404)
	call("GET", "/api/private-knowledge/"+first.ID, admin, nil, 404)
	call("GET", "/api/private-knowledge/"+first.ID, reviewer, nil, 403)
	call("GET", "/api/private-knowledge?view=summary", reviewer, nil, 403)
	var empty struct {
		Items []domain.KnowledgeCardSummary `json:"items"`
		Total int                           `json:"total"`
	}
	json.Unmarshal(call("GET", "/api/private-knowledge?view=summary", peer, nil, 200), &empty)
	if empty.Total != 0 || len(empty.Items) != 0 {
		t.Fatal("cross-owner summary leak")
	}

}
