package handlers

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"learnix/internal/db"
)

// seedChain inserts n alternating user/assistant messages chained by parent_id
// and returns them in order.
func (te *testEnv) seedChain(t *testing.T, chatID int64, n int) []db.ChatMessage {
	t.Helper()
	var out []db.ChatMessage
	var parent int64
	for i := 0; i < n; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		m := &db.ChatMessage{ChatID: chatID, ParentID: parent, Role: role, Content: "msg " + strconv.Itoa(i+1)}
		if err := te.chats.AddMessage(testCtx, m); err != nil {
			t.Fatalf("seed message: %v", err)
		}
		parent = m.ID
		out = append(out, *m)
	}
	return out
}

func TestChatBranch_CopiesPrefix(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "cbranch@test.com", "hunter2!")
	loc := te.createStudy(t, "genetica", cookie)
	te.req(t, "GET", loc, "", cookie)
	sid := fid64(t, loc)
	orig := te.firstChat(t, sid)
	chain := te.seedChain(t, orig.ID, 4)

	// Branch from the 2nd message → new chat with exactly the first 2.
	rr := te.req(t, "POST", loc+"/chats/"+strconv.FormatInt(orig.ID, 10)+
		"/messages/"+strconv.FormatInt(chain[1].ID, 10)+"/branch", "", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("branch: expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Ramificação de") {
		t.Errorf("pane should show the branched chat title, body=%.200s", rr.Body.String())
	}

	chats := mustListChats(t, te, sid)
	if len(chats) != 2 {
		t.Fatalf("expected 2 chats after branch, got %d", len(chats))
	}
	var branch db.Chat
	for _, c := range chats {
		if c.ID != orig.ID {
			branch = c
		}
	}
	if !strings.HasPrefix(branch.Title, "Ramificação de ") {
		t.Errorf("branch title = %q", branch.Title)
	}
	bmsgs, _ := te.chats.Messages(testCtx, branch.ID)
	if len(bmsgs) != 2 {
		t.Fatalf("branch should copy exactly the prefix (2 messages), got %d", len(bmsgs))
	}
	if bmsgs[0].Content != "msg 1" || bmsgs[1].Content != "msg 2" {
		t.Errorf("branch contents wrong: %+v", bmsgs)
	}
	if bmsgs[0].Role != "user" || bmsgs[1].Role != "assistant" {
		t.Errorf("branch roles wrong: %+v", bmsgs)
	}
	if bmsgs[1].ParentID != bmsgs[0].ID {
		t.Errorf("branch parent chain broken: %+v", bmsgs)
	}

	// Original untouched.
	omsgs, _ := te.chats.Messages(testCtx, orig.ID)
	if len(omsgs) != 4 {
		t.Errorf("original chat should keep 4 messages, got %d", len(omsgs))
	}
}

func TestChatBranch_ForeignMessage404(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "fmsg@test.com", "hunter2!")
	loc := te.createStudy(t, "tema", cookie)
	te.req(t, "GET", loc, "", cookie)
	sid := fid64(t, loc)
	a := te.firstChat(t, sid)
	chainA := te.seedChain(t, a.ID, 2)

	rr := te.req(t, "POST", loc+"/chats", "", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("create chat B: %d", rr.Code)
	}
	var b db.Chat
	for _, c := range mustListChats(t, te, sid) {
		if c.ID != a.ID {
			b = c
		}
	}

	// Message of chat A addressed via chat B's route → 404.
	rr = te.req(t, "POST", loc+"/chats/"+strconv.FormatInt(b.ID, 10)+
		"/messages/"+strconv.FormatInt(chainA[0].ID, 10)+"/branch", "", cookie)
	if rr.Code != http.StatusNotFound {
		t.Errorf("foreign message branch: expected 404, got %d", rr.Code)
	}
	if chats := mustListChats(t, te, sid); len(chats) != 2 {
		t.Errorf("no new chat should be created, got %d", len(chats))
	}
}

func TestChatBranch_CrossUser404(t *testing.T) {
	te := newTestEnv(t)
	cookieA := te.register(t, "ba@test.com", "hunter2!")
	locA := te.createStudy(t, "tema A", cookieA)
	te.req(t, "GET", locA, "", cookieA)
	a := te.firstChat(t, fid64(t, locA))
	chain := te.seedChain(t, a.ID, 2)

	cookieB := te.register(t, "bb@test.com", "hunter2!")
	rr := te.req(t, "POST", locA+"/chats/"+strconv.FormatInt(a.ID, 10)+
		"/messages/"+strconv.FormatInt(chain[0].ID, 10)+"/branch", "", cookieB)
	if rr.Code != http.StatusNotFound {
		t.Errorf("branch as other user: expected 404, got %d", rr.Code)
	}
}
