package db

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"learnix/internal/elements"
	"learnix/internal/session"
)

func TestUserRepo_CRUD(t *testing.T) {
	db, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := context.Background()
	r := NewUserRepo(db)

	id, err := r.Create(ctx, "a@b.c", "hashed")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if id == 0 {
		t.Fatal("id zero")
	}

	u, err := r.ByEmail(ctx, "a@b.c")
	if err != nil || u == nil {
		t.Fatalf("by email: %v %v", err, u)
	}
	if u.ID != id || u.PasswordHash != "hashed" {
		t.Errorf("got %+v", u)
	}

	u2, err := r.ByEmail(ctx, "missing@x.com")
	if err != nil || u2 != nil {
		t.Errorf("missing should be nil nil, got %v %v", u2, err)
	}

	u3, err := r.ByID(ctx, id)
	if err != nil || u3 == nil || u3.Email != "a@b.c" {
		t.Fatalf("by id: %v %v", err, u3)
	}
}

func TestSessionRepo_CRUD(t *testing.T) {
	db, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := context.Background()
	ur := NewUserRepo(db)
	sr := NewSessionRepo(db)

	uid, _ := ur.Create(ctx, "s@x.c", "h")
	sid := "sess-abc"
	if err := sr.Create(ctx, sid, uid); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := sr.Get(ctx, sid)
	if err != nil || got == nil || got.UserID != uid {
		t.Fatalf("get: %v %v", err, got)
	}
	if err := sr.Delete(ctx, sid); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got, _ := sr.Get(ctx, sid); got != nil {
		t.Fatal("should be gone")
	}
}

// M1: expired sessions (and legacy rows without expiry) must be rejected and
// purged.
func TestSessionRepo_Expiry(t *testing.T) {
	db, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := context.Background()
	ur := NewUserRepo(db)
	sr := NewSessionRepo(db)

	uid, _ := ur.Create(ctx, "exp@x.c", "h")

	if _, err := db.ExecContext(ctx,
		`INSERT INTO sessions(id, user_id, expires_at) VALUES('expired', ?, datetime('now', '-1 day'))`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO sessions(id, user_id, expires_at) VALUES('legacy', ?, NULL)`, uid); err != nil {
		t.Fatal(err)
	}

	if got, _ := sr.Get(ctx, "expired"); got != nil {
		t.Error("expired session must not be returned")
	}
	if got, _ := sr.Get(ctx, "legacy"); got != nil {
		t.Error("legacy session without expiry must not be returned")
	}

	if err := sr.PurgeExpired(ctx); err != nil {
		t.Fatalf("purge: %v", err)
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("purge should remove both rows, %d remain", n)
	}

	// Fresh sessions are valid and carry a future expiry.
	if err := sr.Create(ctx, "fresh", uid); err != nil {
		t.Fatal(err)
	}
	if got, _ := sr.Get(ctx, "fresh"); got == nil {
		t.Error("fresh session should be valid")
	}
	var expires string
	if err := db.QueryRowContext(ctx, `SELECT expires_at FROM sessions WHERE id='fresh'`).Scan(&expires); err != nil {
		t.Fatal(err)
	}
	if expires == "" {
		t.Error("fresh session must have expires_at set")
	}
}

func TestConfigRepo_UpsertGet(t *testing.T) {
	db, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := context.Background()
	ur := NewUserRepo(db)
	cr := NewConfigRepo(db)

	uid, _ := ur.Create(ctx, "c@x.c", "h")

	cfg, err := cr.Get(ctx, uid)
	if err != nil || cfg != nil {
		t.Fatalf("initial get should be nil nil, got %v %v", cfg, err)
	}

	in := UserConfig{UserID: uid, BaseURL: "https://x", APIKey: "sk-x", Model: "m"}
	if err := cr.Upsert(ctx, in); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	out, err := cr.Get(ctx, uid)
	if err != nil || out == nil {
		t.Fatalf("get after upsert: %v %v", err, out)
	}
	if out.BaseURL != "https://x" || out.Model != "m" || out.APIKey != "sk-x" {
		t.Errorf("round trip mismatch: %+v", out)
	}

	in.Model = "other"
	if err := cr.Upsert(ctx, in); err != nil {
		t.Fatalf("upsert 2: %v", err)
	}
	out2, _ := cr.Get(ctx, uid)
	if out2.Model != "other" {
		t.Errorf("update failed: %+v", out2)
	}
}

func TestStudyRepo_CreateGetUpdate(t *testing.T) {
	db, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := context.Background()
	ur := NewUserRepo(db)
	sr := NewStudyRepo(db)

	uid, _ := ur.Create(ctx, "st@x.c", "h")

	s := &Study{UserID: uid, Topic: "logaritmos", Model: "m", Phase: "study"}
	if err := sr.Create(ctx, s); err != nil {
		t.Fatalf("create: %v", err)
	}
	if s.ID == 0 {
		t.Fatal("id not set")
	}

	got, err := sr.Get(ctx, s.ID)
	if err != nil || got == nil {
		t.Fatalf("get: %v %v", err, got)
	}
	if got.Topic != "logaritmos" || got.Phase != "study" || got.UserID != uid {
		t.Errorf("round trip mismatch: %+v", got)
	}

	got.Phase = "quiz"
	got.Feedback = "fb"
	got.Reviewing = true
	got.History = []session.Message{{Role: "user", Content: "oi"}}
	got.Chat = []session.ChatMsg{{Role: "user", Content: "oi"}}
	if err := sr.Update(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	got2, _ := sr.Get(ctx, s.ID)
	if got2.Phase != "quiz" || got2.Feedback != "fb" || !got2.Reviewing {
		t.Errorf("update mismatch: %+v", got2)
	}
	if len(got2.History) != 1 || len(got2.Chat) != 1 {
		t.Errorf("history/chat not persisted: %+v %+v", got2.History, got2.Chat)
	}
}

func TestStudyRepo_ListAndDelete(t *testing.T) {
	db, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := context.Background()
	ur := NewUserRepo(db)
	sr := NewStudyRepo(db)

	uidA, _ := ur.Create(ctx, "a@x.c", "h")
	uidB, _ := ur.Create(ctx, "b@x.c", "h")

	s := &Study{UserID: uidA, Topic: "segredo", Phase: "study"}
	if err := sr.Create(ctx, s); err != nil {
		t.Fatalf("create: %v", err)
	}

	listA, err := sr.ListByUser(ctx, uidA)
	if err != nil || len(listA) != 1 {
		t.Fatalf("list A: %v %d", err, len(listA))
	}
	if listA[0].Topic != "segredo" {
		t.Errorf("list topic: %+v", listA[0])
	}

	listB, _ := sr.ListByUser(ctx, uidB)
	if len(listB) != 0 {
		t.Errorf("B should not see A's studies: %+v", listB)
	}

	// B cannot delete A's study.
	if err := sr.Delete(ctx, s.ID, uidB); err != nil {
		t.Fatalf("delete (noop): %v", err)
	}
	if got, _ := sr.Get(ctx, s.ID); got == nil {
		t.Fatal("study should still exist after other-user delete")
	}

	if err := sr.Delete(ctx, s.ID, uidA); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got, _ := sr.Get(ctx, s.ID); got != nil {
		t.Fatal("study should be gone")
	}
}

func TestQuizRepo_SaveGetLatest(t *testing.T) {
	db, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := context.Background()
	ur := NewUserRepo(db)
	sr := NewStudyRepo(db)
	qr := NewQuizRepo(db)

	uid, _ := ur.Create(ctx, "q@x.c", "h")
	st := &Study{UserID: uid, Topic: "math", Phase: "quiz"}
	if err := sr.Create(ctx, st); err != nil {
		t.Fatalf("study create: %v", err)
	}

	questions := []session.Question{
		{Text: "Q1", Context: "C1", Elements: []elements.Element{{Type: elements.TableType, Columns: []string{"A"}, Rows: [][]string{{"B"}}}}, Options: []string{"a", "b", "c", "d", "e"}, Correct: 1, Explanation: "E1"},
		{Text: "Q2", Context: "", Options: []string{"a", "b", "c", "d", "e"}, Correct: 0, Explanation: "E2"},
	}
	q := &Quiz{
		UserID:      uid,
		StudyID:     st.ID,
		Topic:       "math",
		Phase:       "quiz",
		Current:     0,
		Questions:   questions,
		Answers:     []int{-1, -1},
		Confidence:  []int{3, 4},
		Assessments: []string{"guess", "attention"},
		TraceJSON:   `{"topic":"math","web":true,"research_brief":"brief"}`,
	}
	if err := qr.Save(ctx, q); err != nil {
		t.Fatalf("save: %v", err)
	}
	if q.ID == 0 {
		t.Fatal("id not set")
	}

	got, err := qr.Get(ctx, q.ID)
	if err != nil || got == nil {
		t.Fatalf("get: %v %v", err, got)
	}
	if len(got.Questions) != 2 || got.Questions[0].Explanation != "E1" {
		t.Errorf("questions round trip: %+v", got.Questions)
	}
	if len(got.Questions[0].Elements) != 1 || got.Questions[0].Elements[0].Type != elements.TableType {
		t.Errorf("question elements round trip: %+v", got.Questions[0].Elements)
	}
	if got.StudyID != st.ID {
		t.Errorf("study id mismatch: %d", got.StudyID)
	}
	if len(got.Confidence) != 2 || got.Confidence[0] != 3 || got.Confidence[1] != 4 || got.Assessments[1] != "attention" || got.TraceJSON == "" {
		t.Errorf("diagnostic data round trip: %+v", got)
	}

	got.Answers = []int{1, 0}
	got.Current = 1
	if err := qr.Save(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	got2, _ := qr.Get(ctx, q.ID)
	if got2.Current != 1 || got2.Answers[0] != 1 {
		t.Errorf("update failed: %+v", got2)
	}

	latest, err := qr.GetLatestByStudy(ctx, st.ID)
	if err != nil || latest == nil || latest.ID != q.ID {
		t.Fatalf("latest: %v %+v", err, latest)
	}
}

// Exam-simulation columns (exam flag, server-authoritative deadline, review
// flags) must survive a save/load round trip on both quiz SELECTs, and rows
// without them must read back as plain quizzes.
func TestQuizRepo_ExamColumnsRoundTrip(t *testing.T) {
	db, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := context.Background()
	ur := NewUserRepo(db)
	tr := NewTestRepo(db)
	qr := NewQuizRepo(db)

	uid, _ := ur.Create(ctx, "exam@x.c", "h")
	definition := &TestDefinition{UserID: uid, Topic: "simulado", Mode: "practice", Preset: "fast", Exam: true}
	if err := tr.Create(ctx, definition); err != nil {
		t.Fatalf("test create: %v", err)
	}
	gotDefinition, err := tr.Get(ctx, uid, definition.ID)
	if err != nil || gotDefinition == nil || !gotDefinition.Exam {
		t.Fatalf("test definition exam flag round trip: %+v (%v)", gotDefinition, err)
	}
	summaries, err := tr.ListByUser(ctx, uid)
	if err != nil || len(summaries) != 1 || !summaries[0].Exam {
		t.Fatalf("test summary exam flag round trip: %+v (%v)", summaries, err)
	}

	deadline := time.Now().UTC().Add(15 * time.Minute).Truncate(time.Second)
	q := &Quiz{
		UserID: uid, TestID: definition.ID, Topic: "simulado", Phase: "quiz",
		Questions: []session.Question{
			{Text: "Q1", Options: []string{"a", "b"}, Correct: 0},
			{Text: "Q2", Options: []string{"a", "b"}, Correct: 1},
		},
		Answers: []int{-1, -1},
		Exam:    true, ExamDeadline: &deadline, Flags: []bool{true, false},
	}
	if err := qr.Save(ctx, q); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := qr.Get(ctx, q.ID)
	if err != nil || got == nil {
		t.Fatalf("get: %v %v", err, got)
	}
	if !got.Exam || got.ExamDeadline == nil || !got.ExamDeadline.Equal(deadline) {
		t.Errorf("exam flag/deadline round trip: exam=%v deadline=%v want %v", got.Exam, got.ExamDeadline, deadline)
	}
	if len(got.Flags) != 2 || !got.Flags[0] || got.Flags[1] {
		t.Errorf("flags round trip: %+v", got.Flags)
	}

	// The study-scoped SELECT must read the exam columns too.
	sr := NewStudyRepo(db)
	study := &Study{UserID: uid, Topic: "simulado estudo", Phase: "quiz"}
	if err := sr.Create(ctx, study); err != nil {
		t.Fatalf("study create: %v", err)
	}
	studyQuiz := &Quiz{UserID: uid, StudyID: study.ID, Topic: "simulado estudo", Phase: "quiz", Questions: q.Questions, Answers: []int{-1, -1}, Exam: true, ExamDeadline: &deadline, Flags: []bool{false, true}}
	if err := qr.Save(ctx, studyQuiz); err != nil {
		t.Fatalf("save study quiz: %v", err)
	}
	latest, err := qr.GetLatestByStudy(ctx, study.ID)
	if err != nil || latest == nil || !latest.Exam || latest.ExamDeadline == nil || len(latest.Flags) != 2 || !latest.Flags[1] {
		t.Errorf("GetLatestByStudy exam round trip: %+v (%v)", latest, err)
	}

	plain := &Quiz{UserID: uid, TestID: definition.ID, Topic: "simulado", Phase: "quiz", Questions: []session.Question{{Text: "Q", Options: []string{"a", "b"}, Correct: 0}}, Answers: []int{-1}}
	if err := qr.Save(ctx, plain); err != nil {
		t.Fatalf("save plain: %v", err)
	}
	gotPlain, err := qr.Get(ctx, plain.ID)
	if err != nil || gotPlain == nil {
		t.Fatalf("get plain: %v %v", err, gotPlain)
	}
	if gotPlain.Exam || gotPlain.ExamDeadline != nil || len(gotPlain.Flags) != 0 {
		t.Errorf("plain quiz must read back with no exam state: %+v", gotPlain)
	}
}

func TestQuizRepo_DeleteInProgress(t *testing.T) {
	db, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := context.Background()
	ur := NewUserRepo(db)
	sr := NewStudyRepo(db)
	qr := NewQuizRepo(db)
	uid, _ := ur.Create(ctx, "d@x.c", "h")
	st := &Study{UserID: uid, Topic: "t", Phase: "quiz"}
	_ = sr.Create(ctx, st)

	q := &Quiz{UserID: uid, StudyID: st.ID, Topic: "t", Phase: "quiz", Questions: []session.Question{{Text: "x"}}, Answers: []int{-1}}
	if err := qr.Save(ctx, q); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := qr.DeleteByStudyInProgress(ctx, st.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	latest, _ := qr.GetLatestByStudy(ctx, st.ID)
	if latest != nil {
		t.Errorf("expected none, got %+v", latest)
	}
}

func TestQuizRepo_OtherStudyIsolation(t *testing.T) {
	db, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := context.Background()
	ur := NewUserRepo(db)
	sr := NewStudyRepo(db)
	qr := NewQuizRepo(db)

	uid, _ := ur.Create(ctx, "a@x.c", "h")
	stA := &Study{UserID: uid, Topic: "A", Phase: "quiz"}
	stB := &Study{UserID: uid, Topic: "B", Phase: "quiz"}
	_ = sr.Create(ctx, stA)
	_ = sr.Create(ctx, stB)

	q := &Quiz{UserID: uid, StudyID: stA.ID, Topic: "secret", Phase: "quiz", Questions: []session.Question{{Text: "x"}}, Answers: []int{-1}}
	if err := qr.Save(ctx, q); err != nil {
		t.Fatalf("save: %v", err)
	}

	latestB, _ := qr.GetLatestByStudy(ctx, stB.ID)
	if latestB != nil {
		t.Errorf("study B should not see study A's quiz: %+v", latestB)
	}
	latestA, _ := qr.GetLatestByStudy(ctx, stA.ID)
	if latestA == nil || latestA.ID != q.ID {
		t.Errorf("study A should see its own quiz: %+v", latestA)
	}
}

func TestFileRepo(t *testing.T) {
	db, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := context.Background()
	ur := NewUserRepo(db)
	sr := NewStudyRepo(db)
	fr := NewFileRepo(db)

	uid, _ := ur.Create(ctx, "f@x.c", "h")
	st := &Study{UserID: uid, Topic: "files", Phase: "study"}
	if err := sr.Create(ctx, st); err != nil {
		t.Fatalf("study create: %v", err)
	}

	note := &File{StudyID: st.ID, Name: "nota.md", Kind: "note", Content: "v1", ElementsJSON: `[{"type":"table","columns":["A"],"rows":[["B"]]}]`}
	if err := fr.Create(ctx, note); err != nil {
		t.Fatalf("create note: %v", err)
	}
	if note.ID == 0 || note.Size != 2 {
		t.Fatalf("create note id/size: %+v", note)
	}

	versions, err := fr.Versions(ctx, note.ID)
	if err != nil || len(versions) != 1 {
		t.Fatalf("versions after create: %v %d", err, len(versions))
	}
	if versions[0].Content != "v1" || versions[0].Author != "user" || versions[0].Message != "criado" {
		t.Errorf("v1 snapshot: %+v", versions[0])
	}
	if versions[0].ElementsJSON == "" {
		t.Error("initial version must preserve elements")
	}

	folder := &File{StudyID: st.ID, Name: "pasta", Kind: "folder"}
	if err := fr.Create(ctx, folder); err != nil {
		t.Fatalf("create folder: %v", err)
	}

	list, err := fr.ListByStudy(ctx, st.ID)
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %v %d", err, len(list))
	}
	if list[0].Kind != "folder" || list[0].ID != folder.ID {
		t.Errorf("folder should sort first: %+v", list[0])
	}
	if list[1].ID != note.ID {
		t.Errorf("note second: %+v", list[1])
	}

	got, err := fr.Get(ctx, note.ID)
	if err != nil || got == nil || got.Content != "v1" || got.ParentID != 0 {
		t.Fatalf("get: %v %+v", err, got)
	}

	if err := fr.UpdateContent(ctx, note.ID, st.ID, "v2 text", "ai", "resposta"); err != nil {
		t.Fatalf("update content: %v", err)
	}
	got2, _ := fr.Get(ctx, note.ID)
	if got2.Content != "v2 text" || got2.Size != 7 {
		t.Errorf("after update: %+v", got2)
	}
	versions2, _ := fr.Versions(ctx, note.ID)
	if len(versions2) != 2 {
		t.Fatalf("versions after update: %d", len(versions2))
	}
	if versions2[0].Content != "v2 text" || versions2[0].Author != "ai" {
		t.Errorf("newest version first: %+v", versions2[0])
	}

	if err := fr.Rename(ctx, note.ID, st.ID, "renomeada.md"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	got3, _ := fr.Get(ctx, note.ID)
	if got3.Name != "renomeada.md" {
		t.Errorf("rename failed: %+v", got3)
	}

	if err := fr.Move(ctx, note.ID, st.ID, folder.ID); err != nil {
		t.Fatalf("move: %v", err)
	}
	got4, _ := fr.Get(ctx, note.ID)
	if got4.ParentID != folder.ID {
		t.Errorf("move failed: %+v", got4)
	}

	if err := fr.Move(ctx, folder.ID, st.ID, folder.ID); err == nil {
		t.Error("move folder into itself should error")
	}

	first := versions2[1]
	if first.Content != "v1" {
		t.Fatalf("expected original version, got %+v", first)
	}
	if err := fr.RestoreVersion(ctx, note.ID, first.ID, st.ID); err != nil {
		t.Fatalf("restore: %v", err)
	}
	got5, _ := fr.Get(ctx, note.ID)
	if got5.Content != "v1" || got5.Size != 2 {
		t.Errorf("restore content: %+v", got5)
	}
	versions3, _ := fr.Versions(ctx, note.ID)
	if len(versions3) != 3 {
		t.Fatalf("versions after restore: %d", len(versions3))
	}
	if versions3[0].Author != "user" || versions3[0].Content != "v1" ||
		!strings.Contains(versions3[0].Message, "restaurado da versão") {
		t.Errorf("restore snapshot: %+v", versions3[0])
	}

	img := &File{StudyID: st.ID, Name: "x.png", Kind: "image", Mime: "image/png", Data: []byte{1, 2, 3, 4}}
	if err := fr.Create(ctx, img); err != nil {
		t.Fatalf("create image: %v", err)
	}
	if img.Size != 4 {
		t.Errorf("image size: %+v", img)
	}
	gotImg, _ := fr.Get(ctx, img.ID)
	if gotImg.Mime != "image/png" || !bytes.Equal(gotImg.Data, []byte{1, 2, 3, 4}) {
		t.Errorf("image round trip: %+v", gotImg)
	}

	if err := fr.Delete(ctx, note.ID, st.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if gone, _ := fr.Get(ctx, note.ID); gone != nil {
		t.Fatal("note should be gone")
	}
	if vs, _ := fr.Versions(ctx, note.ID); len(vs) != 0 {
		t.Errorf("versions should cascade: %d", len(vs))
	}
}

func TestFileRepo_CrossStudyIsolation(t *testing.T) {
	db, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := context.Background()
	ur := NewUserRepo(db)
	sr := NewStudyRepo(db)
	fr := NewFileRepo(db)

	uid, _ := ur.Create(ctx, "iso@x.c", "h")
	stA := &Study{UserID: uid, Topic: "A", Phase: "study"}
	stB := &Study{UserID: uid, Topic: "B", Phase: "study"}
	_ = sr.Create(ctx, stA)
	_ = sr.Create(ctx, stB)

	note := &File{StudyID: stA.ID, Name: "segredo.md", Kind: "note", Content: "original"}
	if err := fr.Create(ctx, note); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := fr.UpdateContent(ctx, note.ID, stB.ID, "hack", "user", "x"); err == nil {
		t.Error("update with wrong study should error")
	}
	if err := fr.Rename(ctx, note.ID, stB.ID, "hack.md"); err != nil {
		t.Fatalf("rename noop: %v", err)
	}
	if err := fr.Delete(ctx, note.ID, stB.ID); err != nil {
		t.Fatalf("delete noop: %v", err)
	}

	got, _ := fr.Get(ctx, note.ID)
	if got == nil || got.Content != "original" || got.Name != "segredo.md" {
		t.Errorf("row must be unchanged: %+v", got)
	}
	if vs, _ := fr.Versions(ctx, note.ID); len(vs) != 1 {
		t.Errorf("no extra version from other study: %d", len(vs))
	}
}

func TestChatRepo(t *testing.T) {
	db, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := context.Background()
	ur := NewUserRepo(db)
	sr := NewStudyRepo(db)
	cr := NewChatRepo(db)

	uid, _ := ur.Create(ctx, "ch@x.c", "h")
	st := &Study{UserID: uid, Topic: "chats", Phase: "study"}
	_ = sr.Create(ctx, st)

	c := &Chat{StudyID: st.ID, Title: "Chat 1"}
	if err := cr.Create(ctx, c); err != nil {
		t.Fatalf("create: %v", err)
	}
	if c.ID == 0 {
		t.Fatal("id not set")
	}

	list, err := cr.ListByStudy(ctx, st.ID)
	if err != nil || len(list) != 1 || list[0].Title != "Chat 1" {
		t.Fatalf("list: %v %+v", err, list)
	}

	if err := cr.Rename(ctx, c.ID, st.ID, "Novo"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	got, err := cr.Get(ctx, c.ID)
	if err != nil || got == nil || got.Title != "Novo" {
		t.Fatalf("get after rename: %v %+v", err, got)
	}

	m1 := &ChatMessage{ChatID: c.ID, Role: "user", Content: "oi"}
	if err := cr.AddMessage(ctx, m1); err != nil {
		t.Fatalf("add m1: %v", err)
	}
	m2 := &ChatMessage{ChatID: c.ID, ParentID: m1.ID, Role: "assistant", Content: "olá", ElementsJSON: `[{"type":"mind_map","nodes":[{"id":"root","label":"Raiz"}]}]`, UsageJSON: `{"prompt_tokens":8,"completion_tokens":4,"total_tokens":12}`}
	if err := cr.AddMessage(ctx, m2); err != nil {
		t.Fatalf("add m2: %v", err)
	}
	m3 := &ChatMessage{ChatID: c.ID, ParentID: m2.ID, Role: "user", Content: "tudo bem?"}
	if err := cr.AddMessage(ctx, m3); err != nil {
		t.Fatalf("add m3: %v", err)
	}
	m4 := &ChatMessage{ChatID: c.ID, ParentID: m3.ID, Role: "assistant", Content: "tudo"}
	if err := cr.AddMessage(ctx, m4); err != nil {
		t.Fatalf("add m4: %v", err)
	}
	for _, m := range []*ChatMessage{m1, m2, m3, m4} {
		if m.ID == 0 {
			t.Fatal("message id not set")
		}
	}

	msgs, err := cr.Messages(ctx, c.ID)
	if err != nil || len(msgs) != 4 {
		t.Fatalf("messages: %v %d", err, len(msgs))
	}
	if msgs[0].Content != "oi" || msgs[0].ParentID != 0 {
		t.Errorf("first message: %+v", msgs[0])
	}
	if msgs[1].ParentID != m1.ID || msgs[3].ParentID != m3.ID {
		t.Errorf("parent chain: %+v", msgs)
	}
	if msgs[1].ElementsJSON == "" {
		t.Error("chat message elements were not persisted")
	}
	if msgs[1].UsageJSON == "" {
		t.Error("chat message token usage was not persisted")
	}

	if err := cr.SetSaved(ctx, m2.ID, c.ID, true); err != nil {
		t.Fatalf("set saved: %v", err)
	}
	msgs2, _ := cr.Messages(ctx, c.ID)
	if !msgs2[1].Saved || msgs2[0].Saved {
		t.Errorf("saved flag: %+v", msgs2)
	}

	branch, err := cr.BranchFrom(ctx, c.ID, m2.ID, st.ID)
	if err != nil || branch == nil {
		t.Fatalf("branch: %v %+v", err, branch)
	}
	if branch.Title != "Ramificação de Novo" {
		t.Errorf("branch title: %q", branch.Title)
	}
	bmsgs, _ := cr.Messages(ctx, branch.ID)
	if len(bmsgs) != 2 {
		t.Fatalf("branch should copy prefix of 2: %+v", bmsgs)
	}
	if bmsgs[0].Role != "user" || bmsgs[0].Content != "oi" || bmsgs[0].ParentID != 0 {
		t.Errorf("branch msg 1: %+v", bmsgs[0])
	}
	if bmsgs[1].Role != "assistant" || bmsgs[1].Content != "olá" || bmsgs[1].ParentID != bmsgs[0].ID {
		t.Errorf("branch msg 2: %+v", bmsgs[1])
	}
	if bmsgs[1].ElementsJSON == "" {
		t.Error("branch should preserve assistant elements")
	}
	if bmsgs[1].UsageJSON == "" {
		t.Error("branch should preserve assistant token usage")
	}

	origMsgs, _ := cr.Messages(ctx, c.ID)
	if len(origMsgs) != 4 {
		t.Errorf("original chat must be untouched: %d", len(origMsgs))
	}

	long := &Chat{StudyID: st.ID, Title: strings.Repeat("título longo ", 10)}
	_ = cr.Create(ctx, long)
	_ = cr.AddMessage(ctx, &ChatMessage{ChatID: long.ID, Role: "user", Content: "x"})
	lmsgs, _ := cr.Messages(ctx, long.ID)
	longBranch, err := cr.BranchFrom(ctx, long.ID, lmsgs[0].ID, st.ID)
	if err != nil {
		t.Fatalf("branch long: %v", err)
	}
	if n := len([]rune(longBranch.Title)); n > 80 {
		t.Errorf("branch title should cap at 80 runes, got %d", n)
	}

	if err := cr.Delete(ctx, c.ID, st.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if gone, _ := cr.Get(ctx, c.ID); gone != nil {
		t.Fatal("chat should be gone")
	}
	if left, _ := cr.Messages(ctx, c.ID); len(left) != 0 {
		t.Errorf("messages should cascade: %d", len(left))
	}
}

func TestHighlightRepo(t *testing.T) {
	db, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := context.Background()
	ur := NewUserRepo(db)
	sr := NewStudyRepo(db)
	hr := NewHighlightRepo(db)

	uid, _ := ur.Create(ctx, "hl@x.c", "h")
	st := &Study{UserID: uid, Topic: "hl", Phase: "study"}
	_ = sr.Create(ctx, st)

	h1 := &Highlight{StudyID: st.ID, SourceKind: "note", SourceID: 1, Excerpt: "trecho um", Note: "obs"}
	if err := hr.Create(ctx, h1); err != nil {
		t.Fatalf("create: %v", err)
	}
	if h1.ID == 0 {
		t.Fatal("id not set")
	}
	h2 := &Highlight{StudyID: st.ID, SourceKind: "message", SourceID: 5, Excerpt: "trecho dois"}
	if err := hr.Create(ctx, h2); err != nil {
		t.Fatalf("create 2: %v", err)
	}

	list, err := hr.ListByStudy(ctx, st.ID)
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %v %d", err, len(list))
	}
	if list[0].ID != h2.ID {
		t.Errorf("newest first: %+v", list[0])
	}
	if list[1].Excerpt != "trecho um" || list[1].Note != "obs" || list[1].SourceKind != "note" {
		t.Errorf("round trip: %+v", list[1])
	}

	if err := hr.Delete(ctx, h1.ID, st.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	list2, _ := hr.ListByStudy(ctx, st.ID)
	if len(list2) != 1 || list2[0].ID != h2.ID {
		t.Errorf("after delete: %+v", list2)
	}
}

func TestLegacyChatMigration(t *testing.T) {
	db, cleanup := NewTestDB(t)
	defer cleanup()
	ctx := context.Background()
	ur := NewUserRepo(db)
	sr := NewStudyRepo(db)
	cr := NewChatRepo(db)

	uid, _ := ur.Create(ctx, "leg@x.c", "h")
	res, err := db.ExecContext(ctx,
		`INSERT INTO studies(user_id, topic, base_url, api_key, model, phase, feedback, reviewing, history_json, chat_json)
		 VALUES(?,?,?,?,?,?,?,?,?,?)`,
		uid, "Tópico legado", "", "", "", "study", "", 0, "[]",
		`[{"Role":"user","Content":"oi"},{"Role":"ai","Content":"olá"}]`)
	if err != nil {
		t.Fatalf("seed study: %v", err)
	}
	studyID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("seed study id: %v", err)
	}

	if err := migrateLegacyChats(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	chats, err := cr.ListByStudy(ctx, studyID)
	if err != nil || len(chats) != 1 {
		t.Fatalf("chats after migrate: %v %d", err, len(chats))
	}
	if chats[0].Title != "Tópico legado" {
		t.Errorf("chat title: %q", chats[0].Title)
	}

	msgs, err := cr.Messages(ctx, chats[0].ID)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("messages: %v %d", err, len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "oi" || msgs[0].ParentID != 0 {
		t.Errorf("msg 1: %+v", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "olá" || msgs[1].ParentID != msgs[0].ID {
		t.Errorf("msg 2: %+v", msgs[1])
	}

	if err := migrateLegacyChats(ctx, db); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
	chats2, _ := cr.ListByStudy(ctx, studyID)
	if len(chats2) != 1 {
		t.Fatalf("idempotency: %d chats", len(chats2))
	}
	msgs2, _ := cr.Messages(ctx, chats2[0].ID)
	if len(msgs2) != 2 {
		t.Errorf("idempotency messages: %d", len(msgs2))
	}

	st, err := sr.Get(ctx, studyID)
	if err != nil || st == nil {
		t.Fatalf("legacy study: %v %+v", err, st)
	}
	if len(st.Chat) != 2 {
		t.Errorf("legacy chat_json must be untouched: %+v", st.Chat)
	}
}
