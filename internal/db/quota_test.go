package db

import (
	"context"
	"testing"
)

func TestQuotaRepo_GetMissing(t *testing.T) {
	db, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := context.Background()
	ur := NewUserRepo(db)
	qr := NewQuotaRepo(db)

	uid, _ := ur.Create(ctx, "q@x.c", "h")
	q, err := qr.Get(ctx, uid)
	if err != nil || q != nil {
		t.Fatalf("missing should be nil nil, got %v %v", q, err)
	}
	if !q.Exhausted() {
		t.Error("nil quota must count as exhausted")
	}
}

func TestQuotaRepo_SetQuotaGet(t *testing.T) {
	db, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := context.Background()
	ur := NewUserRepo(db)
	qr := NewQuotaRepo(db)

	uid, _ := ur.Create(ctx, "q@x.c", "h")
	if err := qr.SetQuota(ctx, uid, 1000); err != nil {
		t.Fatalf("set quota: %v", err)
	}
	q, err := qr.Get(ctx, uid)
	if err != nil || q == nil {
		t.Fatalf("get: %v %v", err, q)
	}
	if q.UserID != uid || q.Quota != 1000 || q.Used != 0 {
		t.Errorf("round trip mismatch: %+v", q)
	}
	if q.Exhausted() {
		t.Error("fresh quota must not be exhausted")
	}
}

func TestQuotaRepo_SetQuotaNegative(t *testing.T) {
	db, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := context.Background()
	ur := NewUserRepo(db)
	qr := NewQuotaRepo(db)

	uid, _ := ur.Create(ctx, "q@x.c", "h")
	if err := qr.SetQuota(ctx, uid, -1); err == nil {
		t.Fatal("negative quota should error")
	}
	q, _ := qr.Get(ctx, uid)
	if q != nil {
		t.Errorf("no row should be created: %+v", q)
	}
}

func TestQuotaRepo_SetQuotaKeepsUsed(t *testing.T) {
	db, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := context.Background()
	ur := NewUserRepo(db)
	qr := NewQuotaRepo(db)

	uid, _ := ur.Create(ctx, "q@x.c", "h")
	if err := qr.SetQuota(ctx, uid, 500); err != nil {
		t.Fatalf("set quota: %v", err)
	}
	if err := qr.AddUsage(ctx, uid, 100, "chat"); err != nil {
		t.Fatalf("add usage: %v", err)
	}
	if err := qr.SetQuota(ctx, uid, 800); err != nil {
		t.Fatalf("re-set quota: %v", err)
	}
	q, _ := qr.Get(ctx, uid)
	if q == nil || q.Quota != 800 || q.Used != 100 {
		t.Errorf("re-set must keep used: %+v", q)
	}
}

func TestQuotaRepo_AddUsageCreatesRow(t *testing.T) {
	db, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := context.Background()
	ur := NewUserRepo(db)
	qr := NewQuotaRepo(db)

	uid, _ := ur.Create(ctx, "q@x.c", "h")
	if err := qr.AddUsage(ctx, uid, 42, "quiz"); err != nil {
		t.Fatalf("add usage: %v", err)
	}
	q, err := qr.Get(ctx, uid)
	if err != nil || q == nil || q.Quota != 0 || q.Used != 42 {
		t.Fatalf("quota row: %v %+v", err, q)
	}
	if !q.Exhausted() {
		t.Error("used >= quota must be exhausted")
	}
	entries, err := qr.RecentUsage(ctx, 10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("usage log: %v %d", err, len(entries))
	}
	if entries[0].UserID != uid || entries[0].Kind != "quiz" || entries[0].Tokens != 42 {
		t.Errorf("log entry: %+v", entries[0])
	}
}

func TestQuotaRepo_AddUsageIncrements(t *testing.T) {
	db, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := context.Background()
	ur := NewUserRepo(db)
	qr := NewQuotaRepo(db)

	uid, _ := ur.Create(ctx, "q@x.c", "h")
	if err := qr.SetQuota(ctx, uid, 1000); err != nil {
		t.Fatalf("set quota: %v", err)
	}
	if err := qr.AddUsage(ctx, uid, 100, "chat"); err != nil {
		t.Fatalf("add usage 1: %v", err)
	}
	if err := qr.AddUsage(ctx, uid, 50, "quiz"); err != nil {
		t.Fatalf("add usage 2: %v", err)
	}
	q, _ := qr.Get(ctx, uid)
	if q == nil || q.Used != 150 || q.Quota != 1000 || q.LifetimeUsed != 150 {
		t.Errorf("used must accumulate: %+v", q)
	}
}

func TestQuotaRepo_AddUsageNonPositiveNoop(t *testing.T) {
	db, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := context.Background()
	ur := NewUserRepo(db)
	qr := NewQuotaRepo(db)

	uid, _ := ur.Create(ctx, "q@x.c", "h")
	if err := qr.AddUsage(ctx, uid, 0, "chat"); err != nil {
		t.Fatalf("zero tokens: %v", err)
	}
	if err := qr.AddUsage(ctx, uid, -5, "chat"); err != nil {
		t.Fatalf("negative tokens: %v", err)
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_log`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("no log rows expected, got %d", n)
	}
	q, _ := qr.Get(ctx, uid)
	if q != nil {
		t.Errorf("no quota row expected: %+v", q)
	}
}

func TestQuotaRepo_AddUsageInvalidKind(t *testing.T) {
	db, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := context.Background()
	ur := NewUserRepo(db)
	qr := NewQuotaRepo(db)

	uid, _ := ur.Create(ctx, "q@x.c", "h")
	if err := qr.AddUsage(ctx, uid, 10, "essay"); err == nil {
		t.Fatal("invalid kind should error")
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_log`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("no log rows expected, got %d", n)
	}
	q, _ := qr.Get(ctx, uid)
	if q != nil {
		t.Errorf("no quota row expected: %+v", q)
	}
}

func TestQuotaRepo_ResetUsage(t *testing.T) {
	db, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := context.Background()
	ur := NewUserRepo(db)
	qr := NewQuotaRepo(db)

	uid, _ := ur.Create(ctx, "q@x.c", "h")
	if err := qr.SetQuota(ctx, uid, 500); err != nil {
		t.Fatalf("set quota: %v", err)
	}
	if err := qr.AddUsage(ctx, uid, 200, "chat"); err != nil {
		t.Fatalf("add usage: %v", err)
	}
	if err := qr.ResetUsage(ctx, uid); err != nil {
		t.Fatalf("reset: %v", err)
	}
	q, _ := qr.Get(ctx, uid)
	if q == nil || q.Used != 0 || q.Quota != 500 || q.LifetimeUsed != 200 {
		t.Errorf("reset must zero used and keep quota: %+v", q)
	}

	uid2, _ := ur.Create(ctx, "norow@x.c", "h")
	if err := qr.ResetUsage(ctx, uid2); err != nil {
		t.Fatalf("reset no-row should be nil: %v", err)
	}
	q2, _ := qr.Get(ctx, uid2)
	if q2 != nil {
		t.Errorf("reset must not create a row: %+v", q2)
	}
}

func TestQuotaRepo_ListAll(t *testing.T) {
	db, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := context.Background()
	ur := NewUserRepo(db)
	qr := NewQuotaRepo(db)

	uidA, _ := ur.Create(ctx, "a@x.c", "h")
	uidB, _ := ur.Create(ctx, "b@x.c", "h")
	if err := qr.SetQuota(ctx, uidA, 1000); err != nil {
		t.Fatalf("set quota: %v", err)
	}
	if err := qr.AddUsage(ctx, uidA, 250, "chat"); err != nil {
		t.Fatalf("add usage: %v", err)
	}

	list, err := qr.ListAll(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("list all: %v %d", err, len(list))
	}
	if list[0].ID != uidB || list[0].Email != "b@x.c" || list[0].Quota != 0 || list[0].Used != 0 {
		t.Errorf("row-less user must appear first as 0/0: %+v", list[0])
	}
	if list[1].ID != uidA || list[1].Email != "a@x.c" || list[1].Quota != 1000 || list[1].Used != 250 {
		t.Errorf("quota user mismatch: %+v", list[1])
	}
	if list[1].CreatedAt.IsZero() {
		t.Error("created_at must be parsed")
	}
}

func TestQuotaRepo_RecentUsage(t *testing.T) {
	db, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := context.Background()
	ur := NewUserRepo(db)
	qr := NewQuotaRepo(db)

	uid, _ := ur.Create(ctx, "r@x.c", "h")
	for i := 1; i <= 5; i++ {
		if err := qr.AddUsage(ctx, uid, int64(i)*10, "chat"); err != nil {
			t.Fatalf("add usage %d: %v", i, err)
		}
	}

	entries, err := qr.RecentUsage(ctx, 3)
	if err != nil || len(entries) != 3 {
		t.Fatalf("recent: %v %d", err, len(entries))
	}
	if entries[0].Tokens != 50 || entries[1].Tokens != 40 || entries[2].Tokens != 30 {
		t.Errorf("newest first: %+v", entries)
	}
	if entries[0].Email != "r@x.c" {
		t.Errorf("email join: %+v", entries[0])
	}

	all, err := qr.RecentUsage(ctx, 1000)
	if err != nil || len(all) != 5 {
		t.Errorf("clamped high limit should return all rows: %v %d", err, len(all))
	}
	one, err := qr.RecentUsage(ctx, 0)
	if err != nil || len(one) != 1 {
		t.Errorf("clamped low limit should return 1 row: %v %d", err, len(one))
	}
}

func TestQuotaRepo_UserDeleteCascades(t *testing.T) {
	db, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := context.Background()
	ur := NewUserRepo(db)
	qr := NewQuotaRepo(db)

	uid, _ := ur.Create(ctx, "del@x.c", "h")
	if err := qr.SetQuota(ctx, uid, 100); err != nil {
		t.Fatalf("set quota: %v", err)
	}
	if err := qr.AddUsage(ctx, uid, 30, "chat"); err != nil {
		t.Fatalf("add usage 1: %v", err)
	}
	if err := qr.AddUsage(ctx, uid, 20, "quiz"); err != nil {
		t.Fatalf("add usage 2: %v", err)
	}

	if _, err := db.ExecContext(ctx, `DELETE FROM users WHERE id=?`, uid); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_quota`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("user_quota should cascade: %d", n)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_log`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("usage_log should cascade: %d", n)
	}
}
