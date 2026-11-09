//go:build integration

package api

import (
	"aigo/internal/aicheck"
	"aigo/internal/batch"
	"aigo/internal/domain"
	"aigo/internal/evaluator"
	"aigo/internal/generator"
	"aigo/internal/pipeline"
	"aigo/internal/review"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestBrowserP3Harness(t *testing.T) {
	if os.Getenv("AIGO_RUN_BROWSER_P3_HARNESS") != "1" {
		t.Skip("explicit local browser harness opt-in required")
	}
	s, store, h, _, point, _, client, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	checker := aicheck.NewService(client, store, store, store, "browser-controlled")
	s.reviewSvc = review.NewService(store, store, store, s.authSvc)
	s.reviewSvc.RequireAICheck = true
	s.aiCheckSvc = checker
	s.pipe = pipeline.New(generator.NewService(client), evaluator.NewService(), review.NewService(store, store, store, nil), nil, store)
	s.pipe.SetDraftChecker(checker)
	checker.StartWorkers(ctx, 2)
	s.pipe.StartGenerationWorkers(ctx, 2)
	ex := batch.NewLocalExecutorWithConfig(generator.NewService(client), store, store, "browser-controlled", batch.LocalConfig{Concurrency: 2, Context: ctx, Pipeline: true, Checker: checker, CheckMaxAttempts: 3})
	defer ex.Close()
	s.batchSvc = ex
	address := os.Getenv("AIGO_BROWSER_HARNESS_ADDR")
	if address == "" {
		address = "127.0.0.1:18960"
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" {
		t.Fatal("browser harness must stay on loopback")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(h)
	server.Listener.Close()
	server.Listener = listener
	server.Start()
	defer server.Close()
	marker := os.Getenv("AIGO_P3_HARNESS_MARKER")
	if _, err = s.kpSvc.CreatePoint(t.Context(), domain.KnowledgePoint{VersionID: point.VersionID, OutlineCode: "basic.browser", Category: "基础医学", Subject: "生理学", Unit: "测试", SubItem: "测试", Topic: "本轮基础医学拒绝测试"}); err == nil {
		t.Fatal("基础医学不能写入浏览器测试的有效大纲")
	}
	// More than one picker page exercises C2 bulk selection against real APIs.
	for i := 0; i < 60; i++ {
		if _, err = s.kpSvc.CreatePoint(t.Context(), domain.KnowledgePoint{VersionID: point.VersionID, Category: "临床综合", Subject: "消化", Unit: "浏览器验收", Topic: fmt.Sprintf("批量浏览器测试考点%02d", i), OutlineCode: fmt.Sprintf("browser.c2.%03d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(map[string]string{"point_id": point.ID, "version_id": point.VersionID})
	if marker == "" || os.WriteFile(marker, raw, 0600) != nil {
		t.Fatal("harness marker unavailable")
	}
	stop := os.Getenv("AIGO_P3_HARNESS_STOP")
	for {
		if _, err = os.Stat(stop); err == nil {
			break
		}
		select {
		case <-t.Context().Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}
