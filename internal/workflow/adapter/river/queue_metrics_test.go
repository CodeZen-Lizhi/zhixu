package riveradapter

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestQueueDepthUsesAvailableStateAndParameterizedQueue(t *testing.T) {
	query := &queueDepthQueryFake{depth: 7}
	depth, err := QueueDepth(context.Background(), query, " workflow ")
	if err != nil {
		t.Fatal(err)
	}
	if depth != 7 || !strings.Contains(query.sql, "state='available'") || !strings.Contains(query.sql, "scheduled_at<=CURRENT_TIMESTAMP") || strings.Contains(query.sql, "workflow ") || len(query.args) != 1 || query.args[0] != "workflow" {
		t.Fatalf("depth=%d sql=%q args=%v", depth, query.sql, query.args)
	}
}

func TestQueueDepthFailsClosed(t *testing.T) {
	if _, err := QueueDepth(context.Background(), nil, "workflow"); err == nil {
		t.Fatal("nil database accepted")
	}
	if _, err := QueueDepth(context.Background(), &queueDepthQueryFake{err: errors.New("down")}, "workflow"); err == nil {
		t.Fatal("query failure was swallowed")
	}
}

func TestRunningJobCountUsesRunningStateAndParameterizedQueue(t *testing.T) {
	query := &queueDepthQueryFake{depth: 2}
	count, err := RunningJobCount(context.Background(), query, " workflow ")
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 || !strings.Contains(query.sql, "state='running'") || strings.Contains(query.sql, "workflow ") || len(query.args) != 1 || query.args[0] != "workflow" {
		t.Fatalf("count=%d sql=%q args=%v", count, query.sql, query.args)
	}
}

type queueDepthQueryFake struct {
	sql   string
	args  []any
	depth int64
	err   error
}

func (query *queueDepthQueryFake) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	query.sql = sql
	query.args = append([]any(nil), args...)
	return queueDepthRowFake{depth: query.depth, err: query.err}
}

type queueDepthRowFake struct {
	depth int64
	err   error
}

func (row queueDepthRowFake) Scan(destinations ...any) error {
	if row.err != nil {
		return row.err
	}
	destination, ok := destinations[0].(*int64)
	if !ok {
		return errors.New("unexpected destination")
	}
	*destination = row.depth
	return nil
}
