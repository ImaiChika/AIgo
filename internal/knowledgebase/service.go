// Package knowledgebase manages curated SQL cards, with no embedding or retrieval agent.
package knowledgebase

import (
	"aigo/internal/domain"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrDenied   = errors.New("当前身份无知识库操作权限")
	ErrInvalid  = errors.New("知识卡内容无效")
	ErrNotFound = errors.New("知识卡不存在")
	ErrConflict = errors.New("知识卡已被其他窗口修改，请刷新后重试")
)

type Capabilities struct {
	View   bool `json:"view"`
	Manage bool `json:"manage"`
	Use    bool `json:"use"`
}

func CapabilitiesFor(p []string) Capabilities {
	has := func(v string) bool {
		for _, x := range p {
			if x == v {
				return true
			}
		}
		return false
	}
	view := has(domain.PermKBView)
	return Capabilities{view, view && has(domain.PermKBManage), view && has(domain.PermKBUse)}
}

type Filter struct {
	Query, Kind, Status, KnowledgePointID string
	BaseOnly                              bool
	Limit, Offset                         int
}
type Store interface {
	WithKnowledgeBaseAccess(context.Context, string, string, func(context.Context, []string) error) error
	ListKnowledgeCards(context.Context, Filter) ([]domain.KnowledgeCard, int, error)
	GetKnowledgeCard(context.Context, string) (*domain.KnowledgeCard, error)
	KnowledgeCardHistory(context.Context, string) ([]domain.KnowledgeCard, error)
	WriteKnowledgeCard(context.Context, domain.KnowledgeCard, int) error
	ResolveKnowledgeCardPoints(context.Context, []domain.KnowledgePoint) ([]domain.KnowledgePoint, error)
	ResolveKnowledgeImportPoints(context.Context, string, []string) ([]domain.KnowledgePoint, error)
	BeginKnowledgeImport(context.Context, string, string, string, string) (*ImportResult, error)
	FinishKnowledgeImport(context.Context, string, ImportResult) error
}
type Service struct{ Store Store }

func (s *Service) Access(ctx context.Context, uid, role string, fn func(context.Context, Capabilities) error) error {
	return s.Store.WithKnowledgeBaseAccess(ctx, uid, role, func(ctx context.Context, p []string) error { return fn(ctx, CapabilitiesFor(p)) })
}
func (s *Service) List(ctx context.Context, uid, role string, f Filter) (items []domain.KnowledgeCard, total int, caps Capabilities, err error) {
	if f.Limit <= 0 || f.Limit > 100 {
		f.Limit = 50
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	if len(f.Query) > 600 {
		return nil, 0, caps, ErrInvalid
	}
	err = s.Access(ctx, uid, role, func(ctx context.Context, c Capabilities) error {
		caps = c
		if !c.View {
			return ErrDenied
		}
		var e error
		items, total, e = s.Store.ListKnowledgeCards(ctx, f)
		return e
	})
	return
}
func (s *Service) Detail(ctx context.Context, uid, role, id string, history bool) (items []domain.KnowledgeCard, err error) {
	err = s.Access(ctx, uid, role, func(ctx context.Context, c Capabilities) error {
		if !c.View {
			return ErrDenied
		}
		card, e := s.Store.GetKnowledgeCard(ctx, id)
		if e != nil {
			return e
		}
		if card == nil {
			return ErrNotFound
		}
		if history {
			items, e = s.Store.KnowledgeCardHistory(ctx, id)
		} else {
			items = []domain.KnowledgeCard{*card}
		}
		return e
	})
	return
}
func (s *Service) Save(ctx context.Context, uid, role, id string, input domain.KnowledgeCard) (saved domain.KnowledgeCard, err error) {
	err = s.Access(ctx, uid, role, func(ctx context.Context, c Capabilities) error {
		if !c.Manage {
			return ErrDenied
		}
		expected := input.Revision
		now := time.Now().UTC()
		if id == "" {
			if expected != 0 {
				return ErrConflict
			}
			b := make([]byte, 16)
			if _, e := rand.Read(b); e != nil {
				return e
			}
			input.ID = "kc-" + hex.EncodeToString(b)
			input.OriginSubmissionID = ""
			input.Revision = 1
			input.CreatedBy = uid
			input.CreatedAt = now
		} else {
			old, e := s.Store.GetKnowledgeCard(ctx, id)
			if e != nil {
				return e
			}
			if old == nil {
				return ErrNotFound
			}
			if expected != old.Revision {
				return ErrConflict
			}
			if old.Status == "archived" {
				return fmt.Errorf("%w: 归档卡不能编辑，请新建知识卡", ErrInvalid)
			}
			input.ID = id
			input.OriginSubmissionID = old.OriginSubmissionID
			input.Revision = expected + 1
			input.CreatedBy = old.CreatedBy
			input.CreatedAt = old.CreatedAt
		}
		input.Title = strings.TrimSpace(input.Title)
		input.Source = strings.TrimSpace(input.Source)
		input.Status = "draft"
		input.UpdatedBy = uid
		input.UpdatedAt = now
		if input.Content.Expression != nil && input.OriginSubmissionID == "" {
			return fmt.Errorf("%w: 表达要求请从个人知识卡提交共享；全局方法使用通用命题规范", ErrInvalid)
		}
		if e := Validate(input, false); e != nil {
			return e
		}
		points, e := s.Store.ResolveKnowledgeCardPoints(ctx, input.KnowledgePoints)
		if e != nil {
			return e
		}
		input.KnowledgePoints = points
		if e = s.Store.WriteKnowledgeCard(ctx, input, expected); e != nil {
			return e
		}
		saved = input
		return nil
	})
	return
}
func (s *Service) Transition(ctx context.Context, uid, role, id, action string, revision int) (saved domain.KnowledgeCard, err error) {
	err = s.Access(ctx, uid, role, func(ctx context.Context, c Capabilities) error {
		if !c.Manage {
			return ErrDenied
		}
		old, e := s.Store.GetKnowledgeCard(ctx, id)
		if e != nil {
			return e
		}
		if old == nil {
			return ErrNotFound
		}
		if old.Revision != revision {
			return ErrConflict
		}
		card := *old
		switch action {
		case "activate":
			if card.Status != "draft" {
				return fmt.Errorf("%w: 只有草稿可启用", ErrInvalid)
			}
			if e = Validate(card, true); e != nil {
				return e
			}
			card.KnowledgePoints, e = s.Store.ResolveKnowledgeCardPoints(ctx, card.KnowledgePoints)
			if e != nil {
				return e
			}
			card.Status = "active"
		case "archive":
			if card.Status == "archived" {
				return fmt.Errorf("%w: 已归档", ErrInvalid)
			}
			card.Status = "archived"
		default:
			return ErrInvalid
		}
		card.Revision++
		card.UpdatedBy = uid
		card.UpdatedAt = time.Now().UTC()
		if e = s.Store.WriteKnowledgeCard(ctx, card, revision); e != nil {
			return e
		}
		saved = card
		return nil
	})
	return
}
func Validate(c domain.KnowledgeCard, active bool) error {
	invalid := func(msg string) error { return fmt.Errorf("%w: %s", ErrInvalid, msg) }
	if c.Kind != "medical" && c.Kind != "exam_reference" && c.Kind != "custom" && c.Kind != "authoring_guideline" {
		return invalid("请选择考点知识、试题参考、自定义资料或通用命题规范")
	}
	if n := utf8.RuneCountInString(c.Title); n < 1 || n > 120 {
		return invalid("标题须为1—120字")
	}
	if len(c.KnowledgePoints) > 20 {
		return invalid("每卡最多关联20个知识点")
	}
	seen := map[string]bool{}
	for _, p := range c.KnowledgePoints {
		if p.ID == "" || p.VersionID == "" || seen[p.ID] {
			return invalid("知识点须来自有效大纲，且不能重复")
		}
		seen[p.ID] = true
	}
	if utf8.RuneCountInString(c.Source) > 2000 || utf8.RuneCountInString(c.Note) > 2000 || utf8.RuneCountInString(c.Content.CustomText) > 6000 {
		return invalid("来源、备注或自定义内容过长")
	}
	total := utf8.RuneCountInString(c.Content.CustomText)
	if expression := c.Content.Expression; expression != nil {
		if c.Kind != "custom" {
			return invalid("表达要求仅用于自定义资料，医学事实和参考题仍作为资料")
		}
		if utf8.RuneCountInString(expression.Instructions) > 4000 || utf8.RuneCountInString(expression.ExplanationNote) > 160 {
			return invalid("表达要求最多4000字，解析附言最多160字")
		}
		if active && strings.TrimSpace(expression.Instructions) == "" && strings.TrimSpace(expression.ExplanationNote) == "" {
			return invalid("启用前须填写表达要求或解析附言")
		}
		total += utf8.RuneCountInString(expression.Instructions + expression.ExplanationNote)
	}
	for _, lines := range [][]string{c.Content.ExamTargets, c.Content.Features, c.Content.Preconditions, c.Content.Pitfalls} {
		if len(lines) > 30 {
			return invalid("每个维度最多30条")
		}
		for _, v := range lines {
			n := utf8.RuneCountInString(strings.TrimSpace(v))
			if n == 0 || n > 600 {
				return invalid("每条维度内容须为1—600字")
			}
			total += n
		}
	}
	if len(c.Content.Facts) > 30 {
		return invalid("每卡最多30条事实")
	}
	facts := map[string]bool{}
	for _, f := range c.Content.Facts {
		if len(f.ID) == 0 || len(f.ID) > 60 || facts[f.ID] || strings.TrimSpace(f.Text) == "" || utf8.RuneCountInString(f.Text) > 1000 || utf8.RuneCountInString(f.AppliesWhen) > 600 || utf8.RuneCountInString(f.Source) > 1000 {
			return invalid("事实须有唯一编号及有效内容，条件/来源不能过长")
		}
		facts[f.ID] = true
		total += utf8.RuneCountInString(f.Text + f.AppliesWhen + f.Source)
	}
	if ref := c.Content.Reference; ref != nil {
		if c.Kind != "exam_reference" {
			return invalid("仅真题参考可填写参考试题")
		}
		if utf8.RuneCountInString(ref.Year) > 40 {
			return invalid("参考试题的年份或考试名称最多40字")
		}
		if utf8.RuneCountInString(ref.Stem) > 6000 {
			return invalid("参考题干最多6000字")
		}
		if utf8.RuneCountInString(ref.Explanation) > 4000 {
			return invalid("参考解析最多4000字")
		}
		if len(ref.Options) > 5 {
			return invalid("参考试题最多5个选项")
		}
		labels := map[string]bool{}
		for _, o := range ref.Options {
			if len(o.Label) != 1 || o.Label < "A" || o.Label > "E" || labels[o.Label] || strings.TrimSpace(o.Text) == "" || utf8.RuneCountInString(o.Text) > 600 {
				return invalid("参考选项标签、内容或数量不正确")
			}
			labels[o.Label] = true
		}
		if ref.Answer != "" && !labels[ref.Answer] {
			return invalid("参考答案必须对应选项")
		}
		total += utf8.RuneCountInString(ref.Stem + ref.Explanation)
	}
	if total > 18000 {
		return invalid("知识卡总内容最多18000字，请拆分")
	}
	if c.Kind == "authoring_guideline" {
		if len(c.KnowledgePoints) != 0 || len(c.Content.Facts) != 0 || len(c.Content.ExamTargets) != 0 || len(c.Content.Features) != 0 || len(c.Content.Preconditions) != 0 || len(c.Content.Pitfalls) != 0 || c.Content.Reference != nil {
			return invalid("通用命题规范只维护方法正文，不绑定考点或混入医学事实/参考题")
		}
		if utf8.RuneCountInString(c.Content.CustomText) > 1500 {
			return invalid("每张通用命题规范最多1500字，请精简")
		}
		if active && (c.Source == "" || strings.TrimSpace(c.Content.CustomText) == "") {
			return invalid("启用通用命题规范前须填写正文和来源")
		}
	}
	if active {
		if len(c.KnowledgePoints) == 0 && c.Kind != "authoring_guideline" {
			return invalid("启用前须绑定知识点")
		}
		switch c.Kind {
		case "medical":
			if len(c.Content.ExamTargets) == 0 || len(c.Content.Facts) == 0 || c.Source == "" {
				return invalid("考点知识启用前须填写关键考点、事实与来源")
			}
		case "exam_reference":
			if c.Content.Reference == nil || strings.TrimSpace(c.Content.Reference.Stem) == "" || c.Source == "" {
				return invalid("真题参考启用前须填写题干及来源")
			}
		case "custom":
			if strings.TrimSpace(c.Content.CustomText) == "" && c.Content.Expression == nil {
				return invalid("自定义资料启用前须填写内容")
			}
		}
	}
	return nil
}
