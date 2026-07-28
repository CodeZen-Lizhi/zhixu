package main

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const reviewCompositionQuestionRefKey = "review-question-ref-composition-key-2026"

func TestNewReviewHandlerRequiresDatabase(t *testing.T) {
	if handler, err := newReviewHandler(nil, time.Second, reviewCompositionQuestionRefKey); err == nil || handler != nil {
		t.Fatalf("handler=%#v err=%v", handler, err)
	}
}

func TestNewReviewHandlerComposesProductionDependencies(t *testing.T) {
	handler, err := newReviewHandler(&pgxpool.Pool{}, time.Second, reviewCompositionQuestionRefKey)
	if err != nil || handler == nil || !handler.Available() {
		t.Fatalf("handler=%#v err=%v", handler, err)
	}
}
