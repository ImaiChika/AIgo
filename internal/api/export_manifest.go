package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"aigo/internal/auth"
	"aigo/internal/domain"
	"aigo/internal/storage"
)

type exportManifest struct {
	Version   int                      `json:"version"`
	OwnerID   string                   `json:"owner_id"`
	Scope     string                   `json:"scope"`
	Questions []exportManifestQuestion `json:"questions"`
}
type exportManifestQuestion struct {
	ID              string `json:"id"`
	Version         int    `json:"version"`
	LibraryRevision int64  `json:"library_revision"`
	ReviewRevision  int64  `json:"review_revision"`
	ShareID         string `json:"share_id,omitempty"`
	ShareRevision   int64  `json:"share_revision,omitempty"`
}

func (s *Server) currentlyExportable(r *http.Request, q *domain.A2Question, scope string) bool {
	if scope == "global" {
		if q == nil {
			return false
		}
		frozen, share, _, err := s.loadSharedQuestion(r, q.ID)
		return err == nil && share.Status == domain.QuestionShareApproved && frozen.Version == q.Version &&
			s.permissionInQuestionBank(r, frozen, domain.PermQuestionDownload)
	}
	if q == nil || !s.exportableInScope(*q, scope) || !s.matchesQuestionExportScope(r, q, scope) || !s.questionInScope(r, q, domain.PermQuestionDownload) || !s.canViewQuestion(r, q) {
		return false
	}
	if tasks, ok := s.questionStore.(interface {
		HasActiveReviewTask(context.Context, string) (bool, error)
	}); ok {
		active, err := tasks.HasActiveReviewTask(r.Context(), q.ID)
		if err != nil || active {
			return false
		}
	}
	if scope != "personal" {
		return q.LibraryState != domain.LibraryDeleted
	}
	// Migrated published content has explicit historical provenance. New self/
	// review confirmations require the persisted initial check, not a live worker.
	if q.ConfirmationSource == domain.ConfirmSourceLegacy && q.Status == domain.StatusPublished {
		return true
	}
	evidence, ok := s.questionStore.(storage.InitialCheckEvidenceStore)
	if !ok {
		return false
	}
	passed, err := evidence.HasInitialCheckPass(r.Context(), q.ID)
	return err == nil && passed
}

func (s *Server) saveExportManifest(r *http.Request, path, scope string, questions []domain.A2Question) error {
	manifest := exportManifest{Version: 1, OwnerID: auth.GetUserID(r.Context()), Scope: scope}
	for _, q := range questions {
		item := exportManifestQuestion{ID: q.ID, Version: q.Version, LibraryRevision: q.LibraryRevision, ReviewRevision: q.ReviewRevision}
		if scope == "global" {
			frozen, share, _, err := s.loadSharedQuestion(r, q.ID)
			if err != nil || share.Status != domain.QuestionShareApproved || frozen.Version != q.Version {
				return fmt.Errorf("分享资格已变化，请重新导出")
			}
			item.ShareID = share.ID
			item.ShareRevision = share.Revision
		}
		manifest.Questions = append(manifest.Questions, item)
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	// Only the completed data file plus this owned scope/version manifest can be
	// downloaded. A partially written/old file is never a fallback authority.
	temp, err := os.CreateTemp(filepath.Dir(path), ".export-manifest-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err = temp.Chmod(0600); err != nil {
		temp.Close()
		return err
	}
	if _, err = temp.Write(raw); err != nil {
		temp.Close()
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path+".manifest.json")
}

func (s *Server) validateExportManifest(w http.ResponseWriter, r *http.Request, path string) bool {
	raw, err := os.ReadFile(path + ".manifest.json")
	var manifest exportManifest
	if err != nil || json.Unmarshal(raw, &manifest) != nil || manifest.Version != 1 || manifest.OwnerID != auth.GetUserID(r.Context()) || len(manifest.Questions) == 0 {
		writeError(w, 409, "导出文件缺少有效的范围与版本记录，请重新导出")
		return false
	}
	if !s.requireExportAccess(w, r, questionExportRequest{Scope: manifest.Scope}) {
		return false
	}
	for _, item := range manifest.Questions {
		if manifest.Scope == "global" {
			q, share, status, err := s.loadSharedQuestion(r, item.ID)
			if err != nil {
				if status == 403 {
					writeError(w, 403, "共享题库查看权限已变化")
				} else if status == 503 {
					writeError(w, 503, "暂时无法核验共享版本")
				} else {
					writeError(w, 409, "共享版本资格已变化，请重新导出")
				}
				return false
			}
			if !s.permissionInQuestionBank(r, q, domain.PermQuestionDownload) {
				writeError(w, 403, "下载题库范围已变化")
				return false
			}
			if share.ID != item.ShareID || share.Status != domain.QuestionShareApproved || share.Revision != item.ShareRevision || q.Version != item.Version {
				writeError(w, 409, "共享版本资格已变化，请重新导出")
				return false
			}
			continue
		}
		q, err := s.questionStore.GetQuestion(r.Context(), item.ID)
		if err != nil {
			writeError(w, 503, "暂时无法核验导出资格，请稍后重试")
			return false
		}
		if q == nil || q.Version != item.Version || q.LibraryRevision != item.LibraryRevision || q.ReviewRevision != item.ReviewRevision || !s.currentlyExportable(r, q, manifest.Scope) {
			writeError(w, 409, "题目版本、题库归属或导出资格已变化，请重新导出")
			return false
		}
	}
	return true
}
