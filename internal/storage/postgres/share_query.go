package postgres

import (
	"aigo/internal/storage"
	"strings"
)

func isShareFilter(f storage.QuestionFilter) bool { return f.GlobalOnly || len(f.GlobalStatuses) > 0 }

const frozenBankIDsSQL = `CASE WHEN jsonb_typeof(c4s.question_snapshot->'bank_ids')='array' THEN c4s.question_snapshot->'bank_ids' ELSE '[]'::jsonb END`

// Populate the normal row shape from the immutable snapshot so every filter and
// count uses the same content before pagination. No live personal row is joined.
const sharedQuestionFromSQL = `FROM (SELECT * FROM question_share_requests WHERE question_snapshot IS NOT NULL AND question_version>0) c4s
 CROSS JOIN LATERAL jsonb_populate_record(NULL::questions,
 '{"format_version":"","review_revision":0,"generation_workflow_version":"","confirmation_source":"legacy","library_revision":1,"confirmed_by":"","confirmed_at":null,"deleted_by":"","deleted_reason":"","deleted_from_state":"","created_by":""}'::jsonb
 || c4s.question_snapshot || jsonb_build_object(
 'id',c4s.question_id,'owner_id',c4s.owner_id,'version',c4s.question_version,
 'status',CASE c4s.status WHEN 'approved' THEN 'published' WHEN 'rejected' THEN 'rejected' ELSE 'ai_reviewed' END,
 'library_state','active','confirmed_version',c4s.question_version,
 'created_by','','confirmed_by','','deleted_by','','deleted_reason','','deleted_from_state','',
 'system_name',COALESCE(c4s.question_snapshot->>'system',''),
 'generation_workflow_version',COALESCE(c4s.question_snapshot->>'workflow_version',''),
 'search_text',c4s.snapshot_search_text)) q `

func questionQuerySource(f storage.QuestionFilter) string {
	if isShareFilter(f) {
		return sharedQuestionFromSQL
	}
	return "FROM questions q "
}
func questionQueryColumns(f storage.QuestionFilter) string {
	if !isShareFilter(f) {
		return questionSelectColumns
	}
	return strings.Replace(questionSelectColumns, `COALESCE(ARRAY(SELECT m.bank_id FROM question_bank_members m WHERE m.question_id = q.id ORDER BY m.bank_id), '{}') AS bank_ids`, `ARRAY(SELECT jsonb_array_elements_text(`+frozenBankIDsSQL+`)) AS bank_ids`, 1)
}
