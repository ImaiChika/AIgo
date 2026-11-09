package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"aigo/internal/domain"
	"aigo/internal/knowledge"
)

func uploadOutlineFile(t *testing.T, handler http.Handler, token string, fields map[string]string, filename, content string) *httptest.ResponseRecorder {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	part, err := writer.CreateFormFile("files", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/knowledge-points/import", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestOutlineImportPreviewAndBasicExclusionContract(t *testing.T) {
	server, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	server.kpSvc = knowledge.NewService(store)
	handler := server.Handler()
	admin := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.77")
	created := serveAuthJSON(t, handler, "POST", "/api/knowledge-versions", admin, "198.51.100.77", map[string]any{"name": "2026 临床大纲", "year": 2026})
	if created.Code != 201 {
		t.Fatal(created.Code, created.Body)
	}
	var version domain.KnowledgeVersion
	if err := json.Unmarshal(created.Body.Bytes(), &version); err != nil {
		t.Fatal(err)
	}
	csv := "分类,专业/系统,单元,细目,要点,大纲代码\n" +
		"临床综合,一、呼吸系统,肺部感染,肺炎,肺炎的诊断,110.1\n" +
		"基础医学,生理,细胞,膜电位,静息电位,20.1\n" +
		"临床综合,药理,用药,抗菌药,抗菌药物应用原则,21.1\n"

	// 预览：不写入，返回分类计数与矛盾明细。
	preview := uploadOutlineFile(t, handler, admin, map[string]string{"version_id": version.ID, "mode": "merge", "preview": "1"}, "大纲.csv", csv)
	if preview.Code != 200 {
		t.Fatal(preview.Code, preview.Body)
	}
	var previewBody knowledge.ImportPreview
	if err := json.Unmarshal(preview.Body.Bytes(), &previewBody); err != nil {
		t.Fatal(err)
	}
	if previewBody.Accepted != 1 || previewBody.ExcludedBasic != 1 || len(previewBody.Conflicted) != 1 || previewBody.Conflicted[0].Code != "21.1" {
		t.Fatalf("预览计数不符: %+v", previewBody)
	}
	if points, err := server.kpSvc.SearchFiltered(t.Context(), knowledge.KPSearchOptions{VersionID: version.ID}); err != nil || len(points) != 0 {
		t.Fatalf("预览不得写入: %+v %v", points, err)
	}

	// 确认导入：矛盾行使整批拒绝，仍无写入。
	confirmed := uploadOutlineFile(t, handler, admin, map[string]string{"version_id": version.ID, "mode": "merge"}, "大纲.csv", csv)
	if confirmed.Code != 400 || !bytes.Contains(confirmed.Body.Bytes(), []byte("矛盾")) {
		t.Fatalf("矛盾批次应 400: %d %s", confirmed.Code, confirmed.Body)
	}

	// 去掉矛盾行后导入成功，响应含剔除计数。
	cleanCSV := "分类,专业/系统,单元,细目,要点,大纲代码\n" +
		"临床综合,一、呼吸系统,肺部感染,肺炎,肺炎的诊断,110.1\n" +
		"基础医学,生理,细胞,膜电位,静息电位,20.1\n"
	imported := uploadOutlineFile(t, handler, admin, map[string]string{"version_id": version.ID, "mode": "merge"}, "大纲.csv", cleanCSV)
	if imported.Code != 200 {
		t.Fatal(imported.Code, imported.Body)
	}
	var importBody struct {
		Imported      int `json:"imported"`
		ExcludedBasic int `json:"excluded_basic"`
		Total         int `json:"total"`
	}
	if err := json.Unmarshal(imported.Body.Bytes(), &importBody); err != nil {
		t.Fatal(err)
	}
	if importBody.Imported != 1 || importBody.ExcludedBasic != 1 || importBody.Total != 1 {
		t.Fatalf("导入结果不符: %+v", importBody)
	}
	points, err := server.kpSvc.SearchFiltered(t.Context(), knowledge.KPSearchOptions{VersionID: version.ID})
	if err != nil || len(points) != 1 || points[0].Category != "临床综合" {
		t.Fatalf("有效大纲只应有 1 个临床点: %+v %v", points, err)
	}

	// 新增/编辑接口直接拒绝基础与矛盾分类。
	if res := serveAuthJSON(t, handler, "POST", "/api/knowledge-points", admin, "198.51.100.77", map[string]any{"version_id": version.ID, "category": "基础医学", "subject": "生化", "topic": "糖酵解", "outline_code": "20.2"}); res.Code != 400 {
		t.Fatalf("基础新增应 400: %d %s", res.Code, res.Body)
	}
	if res := serveAuthJSON(t, handler, "POST", "/api/knowledge-points", admin, "198.51.100.77", map[string]any{"version_id": version.ID, "category": "临床综合", "subject": "解剖", "topic": "骨结构", "outline_code": "21.2"}); res.Code != 400 {
		t.Fatalf("矛盾新增应 400: %d %s", res.Code, res.Body)
	}
	target := points[0]
	target.Category = "基础医学"
	if res := serveAuthJSON(t, handler, "PUT", "/api/knowledge-points/"+target.ID, admin, "198.51.100.77", target); res.Code != 400 {
		t.Fatalf("编辑为基础分类应 400: %d %s", res.Code, res.Body)
	}
}
