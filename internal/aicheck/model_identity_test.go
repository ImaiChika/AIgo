package aicheck

import (
	"aigo/internal/llm"
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

type modelSwitchResolver struct {
	base  string
	reads atomic.Int32
}

func (r *modelSwitchResolver) ResolveQwenConfig(context.Context, llm.ConfigPurpose) (llm.QwenConfig, bool, error) {
	model := "actual-A"
	if r.reads.Add(1) > 1 {
		model = "later-B"
	}
	return llm.QwenConfig{Deployment: llm.DeploymentLocal, BaseURL: r.base, Model: model}, true, nil
}
func TestFirstCheckStoresTheCalledModelInsteadOfLaterActiveModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"content":"{\"verdict\":\"pass\",\"scores\":{\"scientific\":90,\"logic\":90,\"a2_fit\":90,\"answer\":90},\"issues\":[]}"}}]}`))
	}))
	defer server.Close()
	resolver := &modelSwitchResolver{base: server.URL}
	client := llm.NewDynamicQwenClient(resolver, llm.PurposeAICheck, llm.QwenConfig{})
	svc, questions, _, _ := newTestService(client)
	q := testDraft("actual-model-identity", 1)
	if err := questions.SaveQuestion(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	result, err := svc.CheckQuestion(t.Context(), q.ID)
	if err != nil || result.Model != "actual-A" || resolver.reads.Load() != 1 {
		t.Fatal(result, err, resolver.reads.Load())
	}
}
