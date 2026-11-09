package generator

import (
	"encoding/json"
	"fmt"
	"testing"

	"aigo/internal/knowledgebase"
)

func BenchmarkClinicalKnowledgePrompt(b *testing.B) {
	for _, count := range []int{1, 10, 20} {
		b.Run(fmt.Sprintf("questions_%d", count), func(b *testing.B) {
			req, _ := clinicalWorkflowFixture()
			req.Count = count
			bundle, err := knowledgebase.BundleFor(req.Workflow, req.KnowledgePoints[0].ID)
			if err != nil {
				b.Fatal(err)
			}
			frozen, _ := json.Marshal(req.Workflow)
			_, _, plans, err := knowledgeWorkflowPrompt(req)
			if err != nil {
				b.Fatal(err)
			}
			example := len(clinicalOutputExample(bundle, plans))
			var size int
			b.ReportAllocs()
			for b.Loop() {
				prompt, _, _, err := knowledgeWorkflowPrompt(req)
				if err != nil {
					b.Fatal(err)
				}
				size = len(prompt)
			}
			b.ReportMetric(float64(size), "knowledge_prompt_B")
			b.ReportMetric(float64(example), "output_example_B")
			b.ReportMetric(float64(len(frozen)), "frozen_B")
		})
	}
}
