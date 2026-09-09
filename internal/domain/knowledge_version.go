package domain

import (
	"crypto/sha256"
	"fmt"
	"time"
)

const LegacyKnowledgeVersion = "legacy"

type KnowledgeVersion struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Year        int        `json:"year"`
	Description string     `json:"description"`
	Status      string     `json:"status"` // draft / published
	CreatedAt   time.Time  `json:"created_at"`
	PublishedAt *time.Time `json:"published_at,omitempty"`
	PointCount  int        `json:"point_count"`
}

// KnowledgePointKey identifies an outline code within one syllabus. Old question
// snapshots without a version belong to the migrated legacy syllabus.
func KnowledgePointKey(p KnowledgePoint) string {
	v := p.VersionID
	if v == "" {
		v = LegacyKnowledgeVersion
	}
	code := p.OutlineCode
	if code == "" {
		code = p.ID
	}
	return v + "\x00" + code
}

func ScopedKnowledgeID(versionID, code string) string {
	if versionID == LegacyKnowledgeVersion {
		return code
	}
	sum := sha256.Sum256([]byte(versionID + "\x00" + code))
	return fmt.Sprintf("kp-%x", sum[:16])
}

func ExistingKnowledgeKeys(questions []A2Question) map[string]bool {
	keys := map[string]bool{}
	for _, q := range questions {
		if len(q.KnowledgePoints) == 0 && q.OutlineCode != "" {
			keys[KnowledgePointKey(KnowledgePoint{OutlineCode: q.OutlineCode})] = true
		}
		for _, p := range q.KnowledgePoints {
			if p.OutlineCode != "" || p.ID != "" {
				keys[KnowledgePointKey(p)] = true
			}
		}
	}
	return keys
}
