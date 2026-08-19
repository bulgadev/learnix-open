package db

import (
	"context"
	"testing"
	"time"
)

func TestAnalyticsMetadataAndLeaderboards(t *testing.T) {
	database, cleanup := NewTestDB(t)
	defer cleanup()
	users := NewUserRepo(database)
	profiles := NewProfileRepo(database)
	telemetry := NewTelemetryRepo(database)
	leaderboard := NewLeaderboardRepo(database)
	quotas := NewQuotaRepo(database)

	ctx := context.Background()
	uidA, err := users.Create(ctx, "a@test.com", "hash")
	if err != nil {
		t.Fatal(err)
	}
	uidB, err := users.Create(ctx, "b@test.com", "hash")
	if err != nil {
		t.Fatal(err)
	}
	p, err := profiles.ByUser(ctx, uidA)
	if err != nil || p == nil || p.Slug == "" {
		t.Fatalf("profile = %+v, err=%v", p, err)
	}
	if p2, _ := profiles.BySlug(ctx, p.Slug); p2 == nil || p2.UserID != uidA {
		t.Fatalf("profile slug lookup = %+v", p2)
	}

	if err := telemetry.Record(ctx, TelemetryEvent{
		UserID: uidA, Type: "quiz_answered", ValueInt: 2,
		Metadata: map[string]any{"correct": true},
		Delta:    TelemetryDelta{QuestionsAnswered: 1, CorrectAnswers: 1},
	}); err != nil {
		t.Fatal(err)
	}
	stats, err := telemetry.Stats(ctx, uidA)
	if err != nil || stats == nil || stats.QuestionsAnswered != 1 || stats.CorrectAnswers != 1 {
		t.Fatalf("stats = %+v, err=%v", stats, err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	old := now.AddDate(0, -1, 0)
	if err := leaderboard.Record(ctx, RankedResult{
		QuizID: 101, UserID: uidA, Topic: "álgebra", Preset: "fast", Total: 5,
		Correct: 5, ScoreCents: 1000, WeightCents: 50, WeightedScoreCents: 500,
		FinishedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := leaderboard.Record(ctx, RankedResult{
		QuizID: 102, UserID: uidA, Topic: "álgebra", Preset: "moderate", Total: 10,
		Correct: 8, ScoreCents: 800, WeightCents: 100, WeightedScoreCents: 800,
		FinishedAt: old,
	}); err != nil {
		t.Fatal(err)
	}
	if err := leaderboard.Record(ctx, RankedResult{
		QuizID: 103, UserID: uidB, Topic: "geometria", Preset: "moderate", Total: 10,
		Correct: 10, ScoreCents: 1000, WeightCents: 100, WeightedScoreCents: 1000,
		FinishedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := leaderboard.Record(ctx, RankedResult{
		QuizID: 101, UserID: uidA, Topic: "álgebra", Preset: "fast", Total: 5,
		Correct: 5, ScoreCents: 1000, WeightCents: 50, WeightedScoreCents: 500,
		FinishedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	all, err := leaderboard.ListScore(ctx, nil, nil, 100)
	if err != nil || len(all) != 2 || all[0].Value != 1300 || all[1].Value != 1000 {
		t.Fatalf("all-time score = %+v, err=%v", all, err)
	}
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	monthEnd := monthStart.AddDate(0, 1, 0)
	monthly, err := leaderboard.ListScore(ctx, &monthStart, &monthEnd, 100)
	if err != nil || len(monthly) != 2 || monthly[0].Value != 1000 || monthly[1].Value != 500 {
		t.Fatalf("monthly score = %+v, err=%v", monthly, err)
	}
	quizzes, err := leaderboard.ListQuizzes(ctx, &monthStart, &monthEnd, 100)
	if err != nil || len(quizzes) != 2 || quizzes[0].Value != 1 {
		t.Fatalf("monthly quizzes = %+v, err=%v", quizzes, err)
	}

	if err := quotas.AddUsage(ctx, uidA, 321, "chat"); err != nil {
		t.Fatal(err)
	}
	tokens, err := leaderboard.ListTokens(ctx, nil, nil, 100)
	if err != nil || len(tokens) != 1 || tokens[0].Value != 321 {
		t.Fatalf("tokens = %+v, err=%v", tokens, err)
	}
	if err := users.Delete(ctx, uidA); err != nil {
		t.Fatal(err)
	}
	remaining, err := leaderboard.ListScore(ctx, nil, nil, 100)
	if err != nil || len(remaining) != 1 || remaining[0].Slug == "" {
		t.Fatalf("remaining leaderboard = %+v, err=%v", remaining, err)
	}
}
