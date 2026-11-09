package api

import (
	"testing"

	"aigo/internal/review"
)

func TestPaginateReviewTodo(t *testing.T) {
	items := make([]review.MyTaskItem, 45)

	first, total, more := paginateReviewTodo(items, 1, 20)
	if len(first) != 20 || total != 45 || !more {
		t.Fatalf("第一页结果错误: len=%d total=%d more=%v", len(first), total, more)
	}

	last, total, more := paginateReviewTodo(items, 3, 20)
	if len(last) != 5 || total != 45 || more {
		t.Fatalf("末页结果错误: len=%d total=%d more=%v", len(last), total, more)
	}

	empty, total, more := paginateReviewTodo(items, 4, 20)
	if len(empty) != 0 || total != 45 || more {
		t.Fatalf("越界页结果错误: len=%d total=%d more=%v", len(empty), total, more)
	}
}
