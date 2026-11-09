package review

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"aigo/internal/domain"
	"aigo/internal/storage"
)

type restoreAuthorizer interface {
	AuthorizeReviewRestore(context.Context, string) (string, bool, error)
}

type RestorePreview struct {
	Token       string         `json:"token"`
	Count       int            `json:"count"`
	RecordCount int            `json:"record_count"`
	TaskCount   int            `json:"task_count"`
	Owners      map[string]int `json:"owners"`
	ExpiresAt   time.Time      `json:"expires_at"`
}

type restoreManifest struct {
	ActorID   string                       `json:"actor_id"`
	Identity  string                       `json:"identity"`
	Targets   []domain.ReviewRestoreTarget `json:"targets"`
	ExpiresAt time.Time                    `json:"expires_at"`
}

type RestoreRequest struct {
	OperationID  string `json:"operation_id"`
	PreviewToken string `json:"preview_token"`
	Reason       string `json:"reason"`
}

var restoreOperationID = regexp.MustCompile(`^[a-zA-Z0-9_-]{8,128}$`)

// 大纲代码可含250个汉字；1000题的合法题目/任务ID会使签名清单超过1MiB。
// 保持在HTTP全局10MiB请求上限内，同时拒绝无界凭证。
const maxRestorePreviewTokenBytes = 4 << 20

func (s *Service) SetReviewRestoreSigningKey(key []byte) { s.restoreKey = append([]byte(nil), key...) }

func (s *Service) authorizeRestore(ctx context.Context, actor string) (string, error) {
	auth, ok := s.users.(restoreAuthorizer)
	if !ok {
		return "", domain.ErrReviewRestoreForbidden
	}
	name, allowed, err := auth.AuthorizeReviewRestore(ctx, actor)
	if err != nil {
		return "", err
	}
	if !allowed {
		return "", domain.ErrReviewRestoreForbidden
	}
	return name, nil
}

func (s *Service) restoreStorage() (storage.ReviewRestoreStore, error) {
	store, ok := s.reviewStore.(storage.ReviewRestoreStore)
	if !ok {
		return nil, domain.ErrReviewRestoreUnavailable
	}
	return store, nil
}

func (s *Service) ListRestoreCandidates(ctx context.Context, actor string, f domain.ReviewRestoreFilter, page, size int, details bool) ([]domain.ReviewRestoreCandidate, int, error) {
	if _, err := s.authorizeRestore(ctx, actor); err != nil {
		return nil, 0, err
	}
	store, err := s.restoreStorage()
	if err != nil {
		return nil, 0, err
	}
	if size < 1 || size > 100 {
		size = 20
	}
	if page < 1 {
		page = 1
	}
	return store.SearchReviewRestoreCandidates(ctx, f, page, size, details)
}

func (s *Service) ReviewRestoreOptions(ctx context.Context, actor string) (*domain.ReviewRestoreOptions, error) {
	if _, err := s.authorizeRestore(ctx, actor); err != nil {
		return nil, err
	}
	store, ok := s.reviewStore.(storage.ReviewRestoreOptionsStore)
	if !ok {
		return nil, domain.ErrReviewRestoreUnavailable
	}
	return store.ReviewRestoreOptions(ctx)
}

func (s *Service) PreviewReviewRestore(ctx context.Context, actor, identity string, f domain.ReviewRestoreFilter) (*RestorePreview, error) {
	if _, err := s.authorizeRestore(ctx, actor); err != nil {
		return nil, err
	}
	if len(s.restoreKey) < 32 {
		return nil, domain.ErrReviewRestoreUnavailable
	}
	if f.FlowID == "" && len(f.TaskIDs) == 0 {
		return nil, fmt.Errorf("%w: 请选择题目或指定流程", domain.ErrReviewBadRequest)
	}
	if len(f.TaskIDs) > domain.MaxReviewRestoreQuestions {
		return nil, domain.ErrReviewBadRequest
	}
	seen := map[string]bool{}
	for _, id := range f.TaskIDs {
		if id == "" || seen[id] {
			return nil, domain.ErrReviewBadRequest
		}
		seen[id] = true
	}
	store, err := s.restoreStorage()
	if err != nil {
		return nil, err
	}
	f.EligibleOnly = len(f.TaskIDs) == 0
	items, total, err := store.SearchReviewRestoreCandidates(ctx, f, 1, domain.MaxReviewRestoreQuestions+1, false)
	if err != nil {
		return nil, err
	}
	if total > domain.MaxReviewRestoreQuestions {
		return nil, fmt.Errorf("%w: 一次最多还原%d题，请明确分批选择", domain.ErrReviewBadRequest, domain.MaxReviewRestoreQuestions)
	}
	if len(f.TaskIDs) > 0 && len(items) != len(f.TaskIDs) {
		return nil, domain.ErrReviewConflict
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("%w: 没有可还原的题目", domain.ErrReviewBadRequest)
	}
	manifest := restoreManifest{ActorID: actor, Identity: identity, ExpiresAt: time.Now().Add(5 * time.Minute)}
	result := &RestorePreview{Count: len(items), Owners: map[string]int{}, ExpiresAt: manifest.ExpiresAt}
	for _, c := range items {
		if !c.Eligible {
			return nil, fmt.Errorf("%w: %s", domain.ErrReviewConflict, c.Reason)
		}
		manifest.Targets = append(manifest.Targets, c.Target())
		result.RecordCount += c.RecordCount
		result.TaskCount += c.TaskCount
		result.Owners[c.CreatorUsername]++
	}
	sort.Slice(manifest.Targets, func(i, j int) bool { return manifest.Targets[i].TaskID < manifest.Targets[j].TaskID })
	payload, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, s.restoreKey)
	mac.Write([]byte(encoded))
	result.Token = encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return result, nil
}

func (s *Service) decodeRestorePreview(token, actor, identity string) (*restoreManifest, error) {
	if len(s.restoreKey) < 32 {
		return nil, domain.ErrReviewRestoreUnavailable
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return nil, domain.ErrReviewConflict
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, domain.ErrReviewConflict
	}
	mac := hmac.New(sha256.New, s.restoreKey)
	mac.Write([]byte(parts[0]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return nil, domain.ErrReviewConflict
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, domain.ErrReviewConflict
	}
	var manifest restoreManifest
	if json.Unmarshal(data, &manifest) != nil || manifest.ActorID != actor || manifest.Identity != identity || !time.Now().Before(manifest.ExpiresAt) {
		return nil, domain.ErrReviewConflict
	}
	if len(manifest.Targets) == 0 || len(manifest.Targets) > domain.MaxReviewRestoreQuestions {
		return nil, domain.ErrReviewBadRequest
	}
	return &manifest, nil
}

func (s *Service) RestoreReview(ctx context.Context, actor, identity string, req RestoreRequest) (result *domain.ReviewRestoreResult, err error) {
	req.Reason = strings.TrimSpace(req.Reason)
	if !restoreOperationID.MatchString(req.OperationID) || req.Reason == "" || strings.ContainsRune(req.Reason, 0) || !utf8.ValidString(req.Reason) || utf8.RuneCountInString(req.Reason) > 500 || len(req.PreviewToken) > maxRestorePreviewTokenBytes {
		return nil, domain.ErrReviewBadRequest
	}
	// hash绑定用户、当前身份和原始凭证；同ID的不同意图不能被当作成功重试。
	data, _ := json.Marshal(struct {
		Actor, Identity string
		Request         RestoreRequest
	}{actor, identity, req})
	hash := sha256.Sum256(data)
	requestHash := hex.EncodeToString(hash[:])
	store, err := s.restoreStorage()
	if err != nil {
		return nil, err
	}
	err = s.withReviewMutation(ctx, func(txctx context.Context) error {
		actorName, e := s.authorizeRestore(txctx, actor)
		if e != nil {
			return e
		}
		previous, e := store.GetReviewRestoreOperation(txctx, req.OperationID, actor, requestHash)
		if e != nil {
			return e
		}
		if previous != nil {
			result = previous
			return nil
		}
		manifest, e := s.decodeRestorePreview(req.PreviewToken, actor, identity)
		if e != nil {
			return e
		}
		result, e = store.ApplyReviewRestore(txctx, domain.ReviewRestoreCommand{OperationID: req.OperationID, ActorID: actor, ActorName: actorName, Reason: req.Reason, RequestHash: requestHash, Targets: manifest.Targets})
		return e
	})
	return result, err
}
