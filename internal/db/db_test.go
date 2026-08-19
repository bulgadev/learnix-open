package db

import (
	"context"
	"testing"
)

func TestOpenAndMigrate(t *testing.T) {
	db, cleanup := NewTestDB(t)
	defer cleanup()

	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}

	var names []string
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`)
	if err != nil {
		t.Fatalf("query tables: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		names = append(names, n)
	}
	want := map[string]bool{"users": true, "sessions": true, "user_config": true, "studies": true, "quizzes": true}
	for _, n := range names {
		if want[n] {
			delete(want, n)
		}
	}
	if len(want) > 0 {
		t.Errorf("missing tables: %v", want)
	}
}

func TestMigrateIdempotent(t *testing.T) {
	db, cleanup := NewTestDB(t)
	defer cleanup()
	if err := Migrate(db); err != nil {
		t.Fatalf("re-migrate failed: %v", err)
	}
}
