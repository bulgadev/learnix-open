package db

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRankForThresholdsAndMinimum(t *testing.T) {
	tests := []struct {
		name     string
		sum      int64
		count    int64
		key      string
		average  int64
		eligible bool
	}{
		{name: "empty", key: "bronze", average: 0, eligible: false},
		{name: "below minimum", sum: 4000, count: 4, key: "bronze", average: 1000, eligible: false},
		{name: "prata boundary", sum: 3000, count: 5, key: "prata", average: 600, eligible: true},
		{name: "ouro boundary", sum: 3750, count: 5, key: "ouro", average: 750, eligible: true},
		{name: "token maxxer", sum: 4250, count: 5, key: "token-maxxer", average: 850, eligible: true},
		{name: "knowledge maxer", sum: 4500, count: 5, key: "knowledge-maxer", average: 900, eligible: true},
		{name: "study maxer", sum: 4750, count: 5, key: "study-maxer", average: 950, eligible: true},
		{name: "demotion", sum: 4100, count: 5, key: "ouro", average: 820, eligible: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RankFor(tt.sum, tt.count)
			if got.Key != tt.key || got.AverageScoreCents != tt.average || got.Eligible != tt.eligible {
				t.Fatalf("rank = %+v, want key=%s average=%d eligible=%v", got, tt.key, tt.average, tt.eligible)
			}
		})
	}
}

func TestProfileRepoDetailsStatsAndCascade(t *testing.T) {
	database, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := context.Background()
	users := NewUserRepo(database)
	profiles := NewProfileRepo(database)
	leaderboard := NewLeaderboardRepo(database)

	if err := Migrate(database); err != nil {
		t.Fatalf("idempotent migration: %v", err)
	}
	uid, err := users.Create(ctx, "profile@test.com", "hash")
	if err != nil {
		t.Fatal(err)
	}
	initial, err := profiles.ViewByUser(ctx, uid)
	if err != nil || initial == nil {
		t.Fatalf("initial profile = %+v, err=%v", initial, err)
	}
	if initial.Details.DisplayName != "" || !initial.Details.Visibility[ProfileVisibilityBio] {
		t.Fatalf("initial details = %+v", initial.Details)
	}

	if err := profiles.Update(ctx, uid, ProfileUpdate{
		DisplayName: "Pessoa Estudante",
		Bio:         "Aprendendo todos os dias.",
		Visibility: map[string]bool{
			ProfileVisibilityBio:         false,
			ProfileVisibilityTokensUsed:  false,
			ProfileVisibilityDisplayName: true,
		},
		AvatarMIME: "image/png",
		AvatarData: []byte("png-data"),
	}); err != nil {
		t.Fatal(err)
	}

	finished := time.Now().UTC().Truncate(time.Second)
	for i, score := range []int64{1000, 900, 800, 700, 550} {
		if err := leaderboard.Record(ctx, RankedResult{
			QuizID: int64(100 + i), UserID: uid, Topic: "perfil", Preset: "moderate",
			Total: 10, Correct: int(score / 100), ScoreCents: score, WeightCents: 100,
			WeightedScoreCents: score, FinishedAt: finished.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}
	view, err := profiles.ViewByUser(ctx, uid)
	if err != nil || view == nil {
		t.Fatalf("saved profile = %+v, err=%v", view, err)
	}
	if view.PublicName(false) != "Pessoa Estudante" || view.Details.Bio != "Aprendendo todos os dias." {
		t.Fatalf("saved identity = %+v", view.Details)
	}
	if view.Details.Visibility[ProfileVisibilityBio] || view.Details.Visibility[ProfileVisibilityTokensUsed] {
		t.Fatalf("visibility = %+v", view.Details.Visibility)
	}
	if view.Rank.Key != "ouro" || view.Rank.AverageScoreCents != 790 || view.Rank.QuizCount != 5 {
		t.Fatalf("rank = %+v", view.Rank)
	}
	if len(view.Details.AvatarData) != len("png-data") || view.Details.AvatarMIME != "image/png" {
		t.Fatalf("avatar = %q %q", view.Details.AvatarMIME, view.Details.AvatarData)
	}

	if err := users.Delete(ctx, uid); err != nil {
		t.Fatal(err)
	}
	var details int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM profile_details WHERE user_id=?`, uid).Scan(&details); err != nil {
		t.Fatal(err)
	}
	if details != 0 {
		t.Fatalf("profile details survived user deletion: %d", details)
	}
}

func TestProfileUsernameTagAndMonthlyChange(t *testing.T) {
	database, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := context.Background()
	users := NewUserRepo(database)
	profiles := NewProfileRepo(database)

	uid, err := users.Create(ctx, "handle@test.com", "hash")
	if err != nil {
		t.Fatal(err)
	}
	initial, err := profiles.ByUser(ctx, uid)
	if err != nil || initial == nil || initial.Slug != "user#0001" || initial.Username != "user" || initial.Tag != "0001" {
		t.Fatalf("initial handle = %+v, err=%v", initial, err)
	}
	if got, err := NormalizePublicUsername(" Bulga "); err != nil || got != "bulga" {
		t.Fatalf("normalize username = %q, %v", got, err)
	}
	if _, err := NormalizePublicUsername("bad#name"); err == nil {
		t.Fatal("separator accepted in username")
	}

	if err := profiles.Update(ctx, uid, ProfileUpdate{Username: "Bulga"}); err != nil {
		t.Fatal(err)
	}
	changed, err := profiles.ByUser(ctx, uid)
	if err != nil || changed.Slug != "bulga#0001" || changed.Username != "bulga" {
		t.Fatalf("changed handle = %+v, err=%v", changed, err)
	}
	if err := profiles.Update(ctx, uid, ProfileUpdate{Username: "again"}); !errors.Is(err, ErrUsernameChangeCooldown) {
		t.Fatalf("same-month username change = %v", err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE user_profiles SET slug_changed_at=datetime('now', '-1 month') WHERE user_id=?`, uid); err != nil {
		t.Fatal(err)
	}
	if err := profiles.Update(ctx, uid, ProfileUpdate{Username: "again"}); err != nil {
		t.Fatal(err)
	}
	final, err := profiles.ByUser(ctx, uid)
	if err != nil || final.Slug != "again#0001" {
		t.Fatalf("next-month handle = %+v, err=%v", final, err)
	}
}
