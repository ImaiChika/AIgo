package review

import "context"

type reviewRecordCounter interface {
	CountReviewRecordsByExpert(context.Context, string) (int, error)
}

// CountReviewedBy 只计实际保留的审核记录，审核还原删除后自然扣回。
func (s *Service) CountReviewedBy(ctx context.Context, expert string) (int, error) {
	if store, ok := s.reviewStore.(reviewRecordCounter); ok {
		return store.CountReviewRecordsByExpert(ctx, expert)
	}
	tasks, err := s.reviewStore.ListAllTasks(ctx)
	if err != nil {
		return 0, err
	}
	ids := make([]string, 0, len(tasks))
	for _, task := range tasks {
		ids = append(ids, task.ID)
	}
	records, err := s.reviewStore.ListRecordsByTaskIDs(ctx, ids)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, record := range records {
		if record.ExpertID == expert {
			count++
		}
	}
	return count, nil
}
