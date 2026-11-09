package generator

import (
	"aigo/internal/domain"
	"aigo/internal/knowledgebase"
	"aigo/internal/workflowtrace"
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

type generatorTraceRecorder struct{ rows []workflowtrace.Record }

func (r *generatorTraceRecorder) SaveWorkflowExecution(_ context.Context, record workflowtrace.Record) error {
	r.rows = append(r.rows, record)
	return nil
}
func TestExecutionTracingPreservesMessagesOptionsAndRenderedContent(t *testing.T) {
	for _, version := range []string{"direct", knowledgebase.LegacyWorkflowVersion, knowledgebase.InitialClinicalWorkflowVersion, knowledgebase.WorkflowVersion, knowledgebase.PresentationWorkflowVersion} {
		t.Run(version, func(t *testing.T) {
			req, item := clinicalWorkflowFixture()
			if version == knowledgebase.LegacyWorkflowVersion || version == "direct" {
				req, item = workflowFixture()
				if version == "direct" {
					req.Workflow = nil
					delete(item, "knowledge_basis")
				}
			} else {
				req.Workflow.Version = version
				if version == knowledgebase.PresentationWorkflowVersion {
					req.Workflow.Presentation = &domain.PresentationRequirements{SectionOrder: domain.ClinicalSections()}
				}
			}
			raw, err := json.Marshal([]any{item})
			if err != nil {
				t.Fatal(err)
			}
			one, two := &workflowTestClient{response: string(raw)}, &workflowTestClient{response: string(raw)}
			baseline, err := NewService(one).Generate(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			sink := &generatorTraceRecorder{}
			ctx, execution := workflowtrace.Start(t.Context(), sink, "generate:comparison", "generation", 1, version)
			observed, err := NewService(two).Generate(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			execution.Complete("validated", nil)
			execution.Close()
			if one.calls != 1 || two.calls != 1 || !reflect.DeepEqual(one.messages, two.messages) || !reflect.DeepEqual(one.options, two.options) {
				t.Fatal("tracing changed model input/call count")
			}
			a, b := baseline[0], observed[0]
			if a.ClinicalStem != b.ClinicalStem || a.Explanation != b.Explanation || a.Answer != b.Answer || !reflect.DeepEqual(a.Options, b.Options) || !reflect.DeepEqual(a.GenerationContext, b.GenerationContext) {
				t.Fatal("tracing changed generated content or evidence")
			}
			if len(sink.rows) != 1 || sink.rows[0].Outcome != "validated" || len(sink.rows[0].ModelCalls) != 1 || sink.rows[0].ModelCalls[0].Recorded {
				t.Fatal("mock evidence was misreported", sink.rows)
			}
		})
	}
}
