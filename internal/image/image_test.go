package image

import (
	"testing"

	"aigo/internal/domain"
)

func TestBuildImagePrompt(t *testing.T) {
	prompt := domain.ImagePrompt{
		Subject:     "化脓性关节炎",
		Style:       "医学教材示意图",
		MustInclude: []string{"关节腔积液", "滑膜肿胀"},
		MustExclude: []string{"真实患者", "医院标识"},
	}

	text := buildImagePrompt(prompt)

	if text == "" {
		t.Fatal("buildImagePrompt returned empty string")
	}
	t.Logf("生成的提示词: %s", text)

	// 检查包含关键内容
	if !contains(text, "化脓性关节炎") {
		t.Error("提示词应包含主题")
	}
	if !contains(text, "关节腔积液") {
		t.Error("提示词应包含必须出现的要素")
	}
	if !contains(text, "真实患者") {
		t.Error("提示词应包含不能出现的要素")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
