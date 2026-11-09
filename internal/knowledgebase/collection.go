package knowledgebase

import (
	"aigo/internal/domain"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Separate from Store so existing offline/mocked knowledge services keep their
// original behavior. PostgreSQL implements the complete collection contract.
type CollectionStore interface {
	CollectionReceipt(context.Context, string, string, string, string) (json.RawMessage, error)
	FinishCollectionReceipt(context.Context, string, string, string, string, any) error
	ListPrivateKnowledge(context.Context, string, Filter) ([]domain.PrivateKnowledgeCard, int, error)
	GetPrivateKnowledge(context.Context, string, string, bool) (*domain.PrivateKnowledgeCard, error)
	PrivateKnowledgeHistory(context.Context, string, string) ([]domain.PrivateKnowledgeCard, error)
	WritePrivateKnowledge(context.Context, domain.PrivateKnowledgeCard, int) error
	HasPrivateKnowledgeChoice(context.Context, string) (bool, error)
	SetKnowledgeChoice(context.Context, string, string, string, bool) error
	KnowledgeChoices(context.Context, string) ([]domain.KnowledgeCardRef, error)
	SubmitKnowledgeCard(context.Context, domain.KnowledgeSubmission) (domain.KnowledgeSubmission, error)
	ListKnowledgeSubmissions(context.Context, string, bool, Filter) ([]domain.KnowledgeSubmission, int, error)
	LockKnowledgeSubmissionSources(context.Context, []string) error
	FindApprovedKnowledgeSubmission(context.Context, string) (*domain.KnowledgeSubmission, error)
	GetKnowledgeSubmission(context.Context, string) (*domain.KnowledgeSubmission, error)
	DecideKnowledgeSubmission(context.Context, domain.KnowledgeSubmission) error
	LoadChosenKnowledgeCards(context.Context, string, []string, bool, bool) (map[string]WorkflowCandidates, error)
}

func collectionID(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b[:])
}
func collectionHash(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func contains(p []string, v string) bool {
	for _, x := range p {
		if x == v {
			return true
		}
	}
	return false
}
func collectionFilter(f Filter) Filter {
	if f.Limit < 1 || f.Limit > 100 {
		f.Limit = 30
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	return f
}
func (s *Service) collectionAccess(ctx context.Context, uid, role string, fn func(context.Context, CollectionStore, []string) error) error {
	store, ok := s.Store.(CollectionStore)
	if !ok {
		return fmt.Errorf("知识卡服务未就绪")
	}
	return s.Store.WithKnowledgeBaseAccess(ctx, uid, role, func(tx context.Context, p []string) error { return fn(tx, store, p) })
}
func (s *Service) PrivateList(ctx context.Context, uid, role string, f Filter) (cards []domain.PrivateKnowledgeCard, total int, err error) {
	cards = []domain.PrivateKnowledgeCard{}
	f = collectionFilter(f)
	err = s.collectionAccess(ctx, uid, role, func(tx context.Context, st CollectionStore, p []string) error {
		if !domain.PersonalKnowledgeCapabilitiesFor(p).View {
			return ErrDenied
		}
		var e error
		cards, total, e = st.ListPrivateKnowledge(tx, uid, f)
		return e
	})
	return
}
func (s *Service) PrivateHistory(ctx context.Context, uid, role, id string) (cards []domain.PrivateKnowledgeCard, err error) {
	cards = []domain.PrivateKnowledgeCard{}
	err = s.collectionAccess(ctx, uid, role, func(tx context.Context, st CollectionStore, p []string) error {
		if !domain.PersonalKnowledgeCapabilitiesFor(p).View {
			return ErrDenied
		}
		card, e := st.GetPrivateKnowledge(tx, uid, id, false)
		if e != nil {
			return e
		}
		if card == nil {
			return ErrNotFound
		}
		cards, e = st.PrivateKnowledgeHistory(tx, uid, id)
		return e
	})
	return
}

// origin is supplied only by a verified learning source, never from a manual request.
func (s *Service) SavePrivate(ctx context.Context, uid, role string, req domain.KnowledgeCardSaveRequest, origins []*domain.ExperienceSource) (cards []domain.PrivateKnowledgeCard, err error) {
	cards = []domain.PrivateKnowledgeCard{}
	if len(req.Key) < 1 || len(req.Key) > 120 || len(req.Cards) < 1 || len(req.Cards) > 20 {
		return nil, ErrInvalid
	}
	hash := collectionHash(struct {
		Request domain.KnowledgeCardSaveRequest
		Origins []*domain.ExperienceSource
	}{req, origins})
	err = s.collectionAccess(ctx, uid, role, func(tx context.Context, st CollectionStore, p []string) error {
		if !domain.PersonalKnowledgeCapabilitiesFor(p).Manage {
			return ErrDenied
		}
		raw, e := st.CollectionReceipt(tx, uid, req.Key, "private-save", hash)
		if e != nil {
			return e
		}
		if raw != nil {
			return json.Unmarshal(raw, &cards)
		}
		seen := map[string]bool{}
		for i, input := range req.Cards {
			if input.Kind == "authoring_guideline" {
				return fmt.Errorf("%w: 全局规范只在共享管理端维护", ErrInvalid)
			}
			if input.Status != "draft" && input.Status != "active" && input.Status != "archived" {
				return ErrInvalid
			}
			if input.ID != "" && seen[input.ID] {
				return ErrInvalid
			}
			seen[input.ID] = true
			expected := input.Revision
			now := time.Now().UTC()
			var origin *domain.ExperienceSource
			if input.ID == "" {
				if expected != 0 || input.Status == "archived" {
					return ErrConflict
				}
				input.ID = collectionID("pkc-")
				input.CreatedBy = uid
				input.CreatedAt = now
				input.Revision = 1
				if len(origins) > i {
					origin = origins[i]
				}
			} else {
				old, e := st.GetPrivateKnowledge(tx, uid, input.ID, true)
				if e != nil {
					return e
				}
				if old == nil {
					return ErrNotFound
				}
				if old.Revision != expected {
					return ErrConflict
				}
				input.CreatedBy = old.CreatedBy
				input.CreatedAt = old.CreatedAt
				input.Revision = expected + 1
				origin = old.Origin
			}
			input.OriginSubmissionID = ""
			input.Title = strings.TrimSpace(input.Title)
			input.Source = strings.TrimSpace(input.Source)
			input.UpdatedBy = uid
			input.UpdatedAt = now
			if e = Validate(input, input.Status == "active"); e != nil {
				return e
			}
			// Archived snapshots remain readable even after syllabus deletion.
			if input.Status != "archived" {
				input.KnowledgePoints, e = s.Store.ResolveKnowledgeCardPoints(tx, input.KnowledgePoints)
				if e != nil {
					return e
				}
			}
			card := domain.PrivateKnowledgeCard{KnowledgeCard: input, OwnerID: uid, Origin: origin}
			if e = st.WritePrivateKnowledge(tx, card, expected); e != nil {
				return e
			}
			cards = append(cards, card)
		}
		return st.FinishCollectionReceipt(tx, uid, req.Key, "private-save", hash, cards)
	})
	return
}
func (s *Service) ChooseCard(ctx context.Context, uid, role, scope, id string, selected bool) error {
	return s.collectionAccess(ctx, uid, role, func(tx context.Context, st CollectionStore, p []string) error {
		if scope != "private" && scope != "shared" {
			return ErrInvalid
		}
		if scope == "private" {
			if cap := domain.PersonalKnowledgeCapabilitiesFor(p); !cap.View || !cap.Use {
				return ErrDenied
			}
			card, e := st.GetPrivateKnowledge(tx, uid, id, false)
			if e != nil {
				return e
			}
			if card == nil {
				return ErrNotFound
			}
			if selected && card.Status != "active" {
				return ErrInvalid
			}
		} else {
			if !CapabilitiesFor(p).Use {
				return ErrDenied
			}
			card, e := s.Store.GetKnowledgeCard(tx, id)
			if e != nil {
				return e
			}
			if card == nil {
				return ErrNotFound
			}
			if card.OriginSubmissionID == "" {
				return fmt.Errorf("%w: 系统基础资料沿用默认匹配，不通过个人勾选开关停用", ErrInvalid)
			}
			if selected && (card.Status != "active" || card.Kind == "authoring_guideline") {
				return ErrInvalid
			}
		}
		return st.SetKnowledgeChoice(tx, uid, scope, id, selected)
	})
}
func (s *Service) Choices(ctx context.Context, uid, role string) (refs []domain.KnowledgeCardRef, err error) {
	refs = []domain.KnowledgeCardRef{}
	err = s.collectionAccess(ctx, uid, role, func(tx context.Context, st CollectionStore, p []string) error {
		if !CapabilitiesFor(p).View {
			return ErrDenied
		}
		var e error
		refs, e = st.KnowledgeChoices(tx, uid)
		return e
	})
	return
}

func (s *Service) SubmitCards(ctx context.Context, uid, role string, req domain.KnowledgeShareRequest) (items []domain.KnowledgeSubmission, err error) {
	items = []domain.KnowledgeSubmission{}
	if len(req.Key) < 1 || len(req.Key) > 120 || len(req.Cards) < 1 || len(req.Cards) > 20 {
		return nil, ErrInvalid
	}
	hash := collectionHash(req)
	err = s.collectionAccess(ctx, uid, role, func(tx context.Context, st CollectionStore, p []string) error {
		if !contains(p, domain.PermKBSubmit) || !CapabilitiesFor(p).View || !domain.PersonalKnowledgeCapabilitiesFor(p).View {
			return ErrDenied
		}
		raw, e := st.CollectionReceipt(tx, uid, req.Key, "submit", hash)
		if e != nil {
			return e
		}
		if raw != nil {
			return json.Unmarshal(raw, &items)
		}
		seen := map[string]bool{}
		// Sort before locking so overlapping multi-card requests use the same order.
		refs := append([]domain.KnowledgeCardRef(nil), req.Cards...)
		sort.Slice(refs, func(i, j int) bool { return refs[i].ID < refs[j].ID })
		for _, ref := range refs {
			if ref.ID == "" || seen[ref.ID] {
				return ErrInvalid
			}
			seen[ref.ID] = true
			card, e := st.GetPrivateKnowledge(tx, uid, ref.ID, true)
			if e != nil {
				return e
			}
			if card == nil {
				return ErrNotFound
			}
			if card.Revision != ref.Revision || card.Status == "archived" {
				return ErrConflict
			}
			if e = Validate(card.KnowledgeCard, true); e != nil {
				return e
			}
			resolved, e := s.Store.ResolveKnowledgeCardPoints(tx, card.KnowledgePoints)
			if e != nil {
				return e
			}
			if !samePointRevisions(resolved, card.KnowledgePoints) {
				return fmt.Errorf("%w: 考点已变化，请修订卡片后重新提交", ErrConflict)
			}
			snap := card.KnowledgeCard
			snap.Note = ""
			snap.OriginSubmissionID = "" // Private maintenance notes are not submitted.
			payload := domain.KnowledgeSubmission{ID: collectionID("ks-"), SubmitterID: uid, PrivateCardID: card.ID, PrivateRevision: card.Revision, Card: snap, Status: "pending", CreatedAt: time.Now().UTC()}
			if card.Origin != nil {
				payload.Evidence = card.Origin.Evidence
				payload.OriginalVersion = card.Origin.OriginalVersion
				payload.CurrentVersion = card.Origin.CurrentVersion
			}
			submission, e := st.SubmitKnowledgeCard(tx, payload)
			if e != nil {
				return e
			}
			items = append(items, submission)
		}
		return st.FinishCollectionReceipt(tx, uid, req.Key, "submit", hash, items)
	})
	return
}
func samePointRevisions(a, b []domain.KnowledgePoint) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].VersionID != b[i].VersionID || a[i].Revision != b[i].Revision {
			return false
		}
	}
	return true
}
func (s *Service) Submissions(ctx context.Context, uid, role string, review bool, f Filter) (items []domain.KnowledgeSubmission, total int, err error) {
	items = []domain.KnowledgeSubmission{}
	f = collectionFilter(f)
	err = s.collectionAccess(ctx, uid, role, func(tx context.Context, st CollectionStore, p []string) error {
		if review {
			if !contains(p, domain.PermKBReview) || !CapabilitiesFor(p).View {
				return ErrDenied
			}
		} else if !domain.PersonalKnowledgeCapabilitiesFor(p).View && !contains(p, domain.PermKBSubmit) {
			return ErrDenied
		}
		var e error
		items, total, e = st.ListKnowledgeSubmissions(tx, uid, review, f)
		return e
	})
	return
}
func (s *Service) DecideCards(ctx context.Context, uid, role string, req domain.KnowledgeDecisionRequest) (items []domain.KnowledgeSubmission, err error) {
	items = []domain.KnowledgeSubmission{}
	req.Note = strings.TrimSpace(req.Note)
	if len(req.Key) < 1 || len(req.Key) > 120 || len(req.IDs) < 1 || len(req.IDs) > 20 || (req.Decision != "approved" && req.Decision != "rejected") || utf8.RuneCountInString(req.Note) > 2000 || req.Decision == "rejected" && req.Note == "" {
		return nil, ErrInvalid
	}
	hash := collectionHash(req)
	err = s.collectionAccess(ctx, uid, role, func(tx context.Context, st CollectionStore, p []string) error {
		if !contains(p, domain.PermKBReview) || !CapabilitiesFor(p).View {
			return ErrDenied
		}
		raw, e := st.CollectionReceipt(tx, uid, req.Key, "decision", hash)
		if e != nil {
			return e
		}
		if raw != nil {
			return json.Unmarshal(raw, &items)
		}
		ids := append([]string(nil), req.IDs...)
		sort.Strings(ids)
		if e = st.LockKnowledgeSubmissionSources(tx, ids); e != nil {
			return e
		}
		seen := map[string]bool{}
		for _, id := range ids {
			if seen[id] {
				return ErrInvalid
			}
			seen[id] = true
			item, e := st.GetKnowledgeSubmission(tx, id)
			if e != nil {
				return e
			}
			if item == nil {
				return ErrNotFound
			}
			if item.SubmitterID == uid {
				return fmt.Errorf("%w: 不能审核本人提交的知识卡", ErrDenied)
			}
			if item.Status != "pending" {
				return ErrConflict
			}
			now := time.Now().UTC()
			item.Status = req.Decision
			item.ReviewNote = req.Note
			item.ReviewedBy = uid
			item.ReviewedAt = &now
			if req.Decision == "approved" {
				card := item.Card
				if e = Validate(card, true); e != nil {
					return e
				}
				points, e := s.Store.ResolveKnowledgeCardPoints(tx, card.KnowledgePoints)
				if e != nil {
					return e
				}
				if !samePointRevisions(points, card.KnowledgePoints) {
					return fmt.Errorf("%w: 申请中的考点版本已变化，请驳回后重新整理", ErrConflict)
				}

				previous, e := st.FindApprovedKnowledgeSubmission(tx, item.PrivateCardID)
				if e != nil {
					return e
				}
				expected := 0
				if previous != nil {
					if previous.PrivateRevision >= item.PrivateRevision {
						return fmt.Errorf("%w: 较新版本已通过，旧申请不能覆盖；请驳回此旧申请", ErrConflict)
					}
					old, e := s.Store.GetKnowledgeCard(tx, previous.SharedCardID)
					if e != nil {
						return e
					}
					if old == nil {
						return ErrConflict
					}
					card.ID = old.ID
					expected = old.Revision
					card.Revision = expected + 1
					card.CreatedBy = old.CreatedBy
					card.CreatedAt = old.CreatedAt
				} else {
					card.ID = collectionID("kc-")
					card.Revision = 1
					card.CreatedBy = uid
					card.CreatedAt = now
				}
				card.Status = "active"
				card.OriginSubmissionID = item.ID
				card.Note = ""
				card.UpdatedBy = uid
				card.UpdatedAt = now
				if e = s.Store.WriteKnowledgeCard(tx, card, expected); e != nil {
					return e
				}
				item.SharedRevision = card.Revision
				item.SharedCardID = card.ID
			}
			if e = st.DecideKnowledgeSubmission(tx, *item); e != nil {
				return e
			}
			items = append(items, *item)
		}
		return st.FinishCollectionReceipt(tx, uid, req.Key, "decision", hash, items)
	})
	return
}

// Shared and private grants are checked independently in the current identity.
func (s *Service) workflowAccess(ctx context.Context, uid, role string, fn func(context.Context, bool, bool) error) error {
	return s.Store.WithKnowledgeBaseAccess(ctx, uid, role, func(tx context.Context, p []string) error {
		privateCaps := domain.PersonalKnowledgeCapabilitiesFor(p)
		shared, private := CapabilitiesFor(p).Use, privateCaps.View && privateCaps.Use
		if !shared {
			if !private {
				return ErrDenied
			}
			st, ok := s.Store.(CollectionStore)
			if !ok {
				return ErrDenied
			}
			selected, e := st.HasPrivateKnowledgeChoice(tx, uid)
			if e != nil {
				return e
			}
			if !selected {
				return ErrDenied
			}
		}
		return fn(tx, shared, private)
	})
}
func (s *Service) userWorkflowCandidates(ctx context.Context, uid string, points []domain.KnowledgePoint, shared, private bool) (map[string]WorkflowCandidates, error) {
	result := map[string]WorkflowCandidates{}
	var err error
	if shared {
		result, err = s.workflowCandidates(ctx, points)
		if err != nil {
			return nil, err
		}
	} else {
		for _, p := range points {
			result[p.ID] = WorkflowCandidates{Cards: []domain.KnowledgeCard{}}
		}
	}
	if st, ok := s.Store.(CollectionStore); ok {
		ids := make([]string, len(points))
		for i, p := range points {
			ids[i] = p.ID
		}
		chosen, e := st.LoadChosenKnowledgeCards(ctx, uid, ids, private, shared)
		if e != nil {
			return nil, e
		}
		for _, p := range points {
			entry := result[p.ID]
			entry.Cards = append(entry.Cards, chosen[p.ID].Cards...)
			entry.Total += chosen[p.ID].Total
			result[p.ID] = entry
		}
	}
	return result, nil
}
