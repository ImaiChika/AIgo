package review

import (
	"errors"
	"testing"

	"aigo/internal/domain"
	"aigo/internal/storage"
)

func TestOptionalReviewAcceptsTeacherWordingInBothCompletionModes(t *testing.T) {
	for _, mode := range []string{domain.ReviewCompletionOnLastRound, ""} {
		t.Run(map[bool]string{true: "last_round", false: "legacy_final"}[mode != ""], func(t *testing.T) {
			svc, tasks, questions := newTestService(map[string][]string{"bank-neike": {"r1"}}, map[string]bool{"admin1": true})
			q := testQuestion("bank-neike")
			q.OwnerID = "author"
			q.FormatVersion = domain.A2FormatFocused
			q.Options = append(q.Options, domain.Option{Label: "E", Text: "肺炎"})
			q.ClinicalStem = "患者，男，50岁。突发胸痛2小时。该患者最可能的诊断是"
			q.Explanation = "正确答案为A。根据病例判断。B项、C项、D项、E项均不符合本例特点。故选A。"
			if err := q.ValidateA2ContentForReview(); err != nil {
				t.Fatal(err)
			}
			if err := questions.SaveQuestion(t.Context(), *q); err != nil {
				t.Fatal(err)
			}
			q, _ = questions.GetQuestion(t.Context(), q.ID)
			q.Version = 2
			q.ClinicalStem = "老师编写的临床病例：依据以上线索，应该选择哪个诊断？"
			q.Explanation = "根据病例证据作出诊断，解析由老师自主表述。"
			if err := questions.SaveQuestion(t.Context(), *q); err != nil {
				t.Fatal(err)
			}
			flow := c4ModeFlow(t, mode, 1)
			if err := svc.CreateFlow(t.Context(), flow); err != nil {
				t.Fatal(err)
			}
			task, err := svc.SubmitQuestion(t.Context(), q.ID, flow.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := svc.Review(t.Context(), ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusApproved}); err != nil {
				t.Fatal(err)
			}
			if mode == "" {
				if err := svc.Finalize(t.Context(), FinalizeRequest{TaskID: task.ID, ReviewerID: "admin1", Action: domain.StatusApproved}); err != nil {
					t.Fatal(err)
				}
			}
			stored, _ := questions.GetQuestion(t.Context(), q.ID)
			done, _ := tasks.GetTask(t.Context(), task.ID)
			if done.Status != domain.StatusPublished || stored.ClinicalStem != q.ClinicalStem || stored.Explanation != q.Explanation || stored.Version != 2 {
				t.Fatal("optional review changed or blocked teacher wording")
			}
		})
	}
}

func TestDeletedQuestionCannotEnterOptionalReview(t *testing.T) {
	svc, tasks, questions := newTestService(map[string][]string{"bank-neike": {"r1", "r2"}}, map[string]bool{"admin1": true})
	q := testQuestion("bank-neike")
	q.OwnerID = "author"
	q.LibraryState = domain.LibraryDeleted
	q.LibraryRevision = 4
	q.DeletedReason = domain.DeleteReasonOwnerDelete
	if err := questions.SaveQuestion(t.Context(), *q); err != nil {
		t.Fatal(err)
	}
	flow := testFlow()
	if err := tasks.SaveFlowConfig(t.Context(), flow); err != nil {
		t.Fatal(err)
	}
	if task, err := svc.SubmitQuestion(t.Context(), q.ID, flow.ID); !errors.Is(err, domain.ErrReviewNotSubmittable) || task != nil {
		t.Fatalf("deleted question entered review: task=%+v err=%v", task, err)
	}
	stored, _ := questions.GetQuestion(t.Context(), q.ID)
	if stored.LibraryState != domain.LibraryDeleted || stored.DeletedReason != domain.DeleteReasonOwnerDelete || stored.Status != domain.StatusAIReviewed {
		t.Fatal("submission changed deletion", stored)
	}
	if task, _ := tasks.GetTaskByQuestionID(t.Context(), q.ID); task != nil {
		t.Fatal("deleted question acquired an active task")
	}
}

func TestDeletedPublishedQuestionCannotBeUnpublishedIntoBlockedRevision(t *testing.T) {
	svc, tasks, questions := newTestService(nil, map[string]bool{"admin1": true})
	q := testQuestion("bank-neike")
	q.OwnerID = "author"
	q.Status = domain.StatusPublished
	q.LibraryState = domain.LibraryDeleted
	q.LibraryRevision = 3
	q.DeletedReason = domain.DeleteReasonOwnerDelete
	if err := questions.SaveQuestion(t.Context(), *q); err != nil {
		t.Fatal(err)
	}
	task := domain.ReviewTask{ID: "completed-review", QuestionID: q.ID, Status: domain.StatusPublished, QuestionVersion: 1}
	if err := tasks.SaveTask(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	err := svc.ReturnPublishedForRevision(t.Context(), q.ID, "admin1", "管理员", "需要修改")
	if !errors.Is(err, storage.ErrQuestionLibraryConflict) {
		t.Fatalf("unpublish must reject a deletion tombstone: %v", err)
	}
	stored, _ := questions.GetQuestion(t.Context(), q.ID)
	after, _ := tasks.GetTask(t.Context(), task.ID)
	if stored.Status != domain.StatusPublished || stored.LibraryState != domain.LibraryDeleted || after.Status != domain.StatusPublished {
		t.Fatal("unpublish left deleted question trapped", stored, after)
	}
}

func TestOptionalReviewSuspendsPersonalConfirmationAndKeepsContentVersion(t *testing.T) {
	svc, tasks, questions := newTestService(map[string][]string{"bank-neike": {"r1", "r2"}}, map[string]bool{"admin1": true})
	q := testQuestion("bank-neike")
	q.OwnerID = "author"
	q.LibraryState = domain.LibraryActive
	q.LibraryRevision = 4
	q.ConfirmedVersion = 1
	q.ConfirmationSource = domain.ConfirmSourceSelf
	q.ConfirmedBy = "author"
	if err := questions.SaveQuestion(t.Context(), *q); err != nil {
		t.Fatal(err)
	}
	flow := testFlow()
	if err := tasks.SaveFlowConfig(t.Context(), flow); err != nil {
		t.Fatal(err)
	}
	task, err := svc.SubmitQuestion(t.Context(), q.ID, flow.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := questions.GetQuestion(t.Context(), q.ID)
	if stored.LibraryState != domain.LibraryUnfiled || stored.ConfirmedVersion != 0 || stored.LibraryRevision != 5 || stored.Version != 1 {
		t.Fatalf("personal confirmation survived entering review: %+v", stored)
	}
	if err := svc.Review(t.Context(), ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusRevisionRequired, Opinion: "请修改"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Review(t.Context(), ReviewRequest{TaskID: task.ID, ExpertID: "r2", Action: domain.StatusRevisionRequired, Opinion: "请修改"}); err != nil {
		t.Fatal(err)
	}
	stored, _ = questions.GetQuestion(t.Context(), q.ID)
	if stored.LibraryState != domain.LibraryUnfiled || stored.Status != domain.StatusAIReviewed || stored.ConfirmedVersion != 0 || stored.Version != 1 {
		t.Fatalf("revision request advertised an old personal approval: %+v", stored)
	}
}
