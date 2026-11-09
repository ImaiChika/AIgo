package aicheck

import (
	"aigo/internal/llm"
	"context"
	"testing"
	"time"
)

type samplingCheckClient struct{ options []llm.GenerateOptions }

func (c *samplingCheckClient) Complete(_ context.Context, _ []llm.Message, o llm.GenerateOptions) (string, error) {
	c.options = append(c.options, o)
	return `{"verdict":"pass","scores":{"scientific":90,"logic":90,"a2_fit":90,"answer":90},"issues":[]}`, nil
}
func TestCheckWorkerUsesFrozenSamplingIncludingZeroAndLegacy(t *testing.T) {
	for _, tc := range []struct {
		name        string
		temperature *float64
		want        float64
	}{{"legacy", nil, 0.2}, {"configured", floatPointer(0.6), 0.6}, {"zero", floatPointer(0), 0}} {
		t.Run(tc.name, func(t *testing.T) {
			c := &samplingCheckClient{}
			svc, questions, _, tasks := newTestService(c)
			q := testDraft("sampling-"+tc.name, 1)
			if err := questions.SaveQuestion(t.Context(), q); err != nil {
				t.Fatal(err)
			}
			svc.CheckAsync(q.ID)
			task, e := tasks.ClaimNextCheckTask(t.Context(), time.Minute)
			if e != nil || task == nil {
				t.Fatal(e)
			}
			task.SamplingTemperature = tc.temperature
			svc.executeTask(t.Context(), task)
			if len(c.options) != 1 || c.options[0].Temperature != tc.want || c.options[0].TemperatureSet != (tc.want == 0) {
				t.Fatal("frozen sampling not applied", c.options)
			}
		})
	}
}
func floatPointer(v float64) *float64 { return &v }
