package domain

import "time"

// KnowledgeCardSummary is a read-only list projection, never a save payload.
// Content, private notes and AI-origin evidence are intentionally absent.
type KnowledgeCardSummary struct {
	IsSummary          bool                  `json:"is_summary"`
	ID                 string                `json:"id"`
	Revision           int                   `json:"revision"`
	Kind               string                `json:"kind"`
	HasExpression      bool                  `json:"has_expression,omitempty"`
	Title              string                `json:"title"`
	Status             string                `json:"status"`
	KnowledgePoints    []KnowledgePointLabel `json:"knowledge_points"`
	Excerpt            string                `json:"excerpt"`
	SourceExcerpt      string                `json:"source_excerpt"`
	OriginSubmissionID string                `json:"origin_submission_id,omitempty"`
	Selected           *bool                 `json:"selected,omitempty"`
	UpdatedAt          time.Time             `json:"updated_at"`
}
type KnowledgePointLabel struct {
	ID          string `json:"id"`
	VersionID   string `json:"version_id"`
	VersionName string `json:"version_name"`
	VersionYear int    `json:"version_year"`
	Topic       string `json:"topic"`
}

func KnowledgePreview(text string, limit int) string {
	r := []rune(text)
	if len(r) > limit {
		return string(r[:limit]) + "…"
	}
	return text
}
func SummarizeKnowledgeCard(card KnowledgeCard, selected *bool) KnowledgeCardSummary {
	excerpt := card.Content.CustomText
	if expression := card.Content.Expression; expression != nil {
		excerpt = expression.Instructions
		if excerpt == "" {
			excerpt = expression.ExplanationNote
		}
		if excerpt == "" {
			excerpt = card.Content.CustomText
		}
	}
	if excerpt == "" && len(card.Content.Facts) > 0 {
		excerpt = card.Content.Facts[0].Text
	}
	if excerpt == "" && card.Content.Reference != nil {
		excerpt = card.Content.Reference.Stem
	}
	points := []KnowledgePointLabel{}
	for _, p := range card.KnowledgePoints {
		points = append(points, KnowledgePointLabel{p.ID, p.VersionID, p.VersionName, p.VersionYear, p.Topic})
	}
	return KnowledgeCardSummary{IsSummary: true, ID: card.ID, Revision: card.Revision, Kind: card.Kind, HasExpression: card.Content.Expression != nil, Title: card.Title, Status: card.Status, KnowledgePoints: points, Excerpt: KnowledgePreview(excerpt, 160), SourceExcerpt: KnowledgePreview(card.Source, 200), OriginSubmissionID: card.OriginSubmissionID, Selected: selected, UpdatedAt: card.UpdatedAt}
}
