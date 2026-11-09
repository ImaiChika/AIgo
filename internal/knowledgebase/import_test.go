package knowledgebase

import (
	"errors"
	"strings"
	"testing"
)

func TestImportFileContract(t *testing.T) {
	valid := `{"format":"aigo-knowledge-import-v1","cards":[{"kind":"custom","title":"仅导入测试","knowledge_point_codes":["001"],"content":{"custom_text":"测试资料"},"source":"模拟","note":"待核对"}]}`
	if doc, err := parseImport(valid); err != nil || len(doc.Cards) != 1 {
		t.Fatal(doc, err)
	}
	for _, raw := range []string{
		`null`, `[]`, `{"format":"unknown","cards":[]}`,
		strings.Replace(valid, `"kind":"custom"`, `"kind":"custom","status":"active"`, 1),
		strings.Replace(valid, `"title":"仅导入测试"`, `"title":"仅导入测试","ID":"overwrite"`, 1),
		strings.Replace(valid, `"kind":"custom"`, `"kind":"medical","Kind":"custom"`, 1),
		strings.Replace(valid, `"custom_text":"测试资料"`, `"custom_text":"one","custom_text":"two"`, 1),
		strings.Repeat(" ", MaxImportBytes+1),
		`{"format":"aigo-knowledge-import-v1","cards":[` + strings.Repeat(`{},`, 50) + `{}]}`,
	} {
		if _, err := parseImport(raw); !errors.Is(err, ErrInvalid) {
			t.Fatalf("unsafe/invalid import accepted: %v", err)
		}
	}
}
