//go:build integration

package api

import (
	"aigo/internal/domain"
	"strings"
	"testing"
)

func TestC6LearningComparisonNeverExposesStagingQuestionContent(t *testing.T) {
	for _, status := range []domain.QuestionStatus{domain.StatusAIDraft, domain.StatusAutoChecked} {
		t.Run(string(status), func(t *testing.T) {
			s, h, teacher, _, cleanup := libraryTestEnv(t)
			defer cleanup()
			owner, err := s.authSvc.GetUserByUsername("lib-teacher")
			if err != nil {
				t.Fatal(err)
			}
			q := domain.A2Question{ID: "staging-learning-" + string(status), OwnerID: owner.ID, CreatedBy: "lib-teacher", Version: 1, Status: status, ClinicalStem: "STAGING-PRIVATE-STEM", Options: []domain.Option{{Label: "A", Text: "STAGING-PRIVATE-OPTION"}}, Answer: "A", Explanation: "STAGING-PRIVATE-EXPLANATION"}
			if err := s.questionStore.SaveQuestion(t.Context(), q); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"/api/questions/" + q.ID, "/api/questions/" + q.ID + "/learning-comparison"} {
				response := serveGenerateJSON(t, h, "GET", path, teacher, nil)
				if response.Code != 404 || strings.Contains(response.Body.String(), "STAGING-PRIVATE") {
					t.Fatalf("staging read %s = %d %s", path, response.Code, response.Body)
				}
			}
		})
	}
}

func TestC6LegacyPublishedQuestionKeepsReadOnlyComparison(t *testing.T) {
	s, h, teacher, v, cleanup := libraryTestEnv(t)
	defer cleanup()
	q := reportPublishedQuestion(t, s, h, teacher, v, "legacy-readable-comparison")
	response := serveGenerateJSON(t, h, "GET", "/api/questions/"+q.ID+"/learning-comparison", teacher, nil)
	if response.Code != 200 || !strings.Contains(response.Body.String(), q.ClinicalStem) || strings.Contains(response.Body.String(), "\"can_analyze\":true") {
		t.Fatal(response.Code, response.Body)
	}
}
