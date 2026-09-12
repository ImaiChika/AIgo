package api

import "testing"

func TestValidGenerationRunID(t *testing.T) {
	for _, id := range []string{"gen-12345678", "gen-550e8400-e29b-41d4-a716-446655440000", "client_run_2026"} {
		if !validGenerationRunID(id) {
			t.Errorf("合法运行 ID 被拒绝: %q", id)
		}
	}
	for _, id := range []string{"", "short", "gen/unsafe", "含中文的运行编号"} {
		if validGenerationRunID(id) {
			t.Errorf("非法运行 ID 被接受: %q", id)
		}
	}
}
