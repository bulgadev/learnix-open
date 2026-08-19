package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func fakeOpenAIFirstFailureThenSuccess(reply string) *httptest.Server {
	count := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		if count == 1 {
			http.Error(w, "provider unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", reply)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

func chatTurnViewFromResponse(t *testing.T, rr *httptest.ResponseRecorder) chatTurnResponse {
	t.Helper()
	var view chatTurnResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode turn response: %v body=%s", err, rr.Body.String())
	}
	return view
}

func waitForTurnStatus(t *testing.T, te *testEnv, path string, cookie *http.Cookie) chatTurnResponse {
	t.Helper()
	for i := 0; i < 120; i++ {
		rr := te.req(t, "GET", path, "", cookie)
		if rr.Code == http.StatusOK {
			view := chatTurnViewFromResponse(t, rr)
			if view.Status == "succeeded" || view.Status == "failed" {
				return view
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("chat turn did not reach a terminal state: %s", path)
	return chatTurnResponse{}
}

func TestChatTurn_CreateIsIdempotentAndCompletes(t *testing.T) {
	te := newTestEnv(t)
	srv := fakeOpenAI("resposta persistida")
	defer srv.Close()
	cookie := te.register(t, "turn@test.com", "hunter2!")
	te.grantQuota(t, cookie, 100000)
	loc := te.createStudyAt(t, "tema", srv.URL, cookie)
	te.req(t, "GET", loc, "", cookie)
	sid := fid64(t, loc)
	c := te.firstChat(t, sid)
	base := loc + "/chats/" + strconv.FormatInt(c.ID, 10)
	body := `{"message":"oi","client_key":"same-key","web":false}`
	first := te.reqCT(t, "POST", base+"/turns", "application/json", body, cookie)
	if first.Code != http.StatusAccepted {
		t.Fatalf("create turn: expected 202, got %d body=%s", first.Code, first.Body.String())
	}
	view := chatTurnViewFromResponse(t, first)
	second := te.reqCT(t, "POST", base+"/turns", "application/json", body, cookie)
	if second.Code != http.StatusAccepted {
		t.Fatalf("idempotent create: expected 202, got %d body=%s", second.Code, second.Body.String())
	}
	view2 := chatTurnViewFromResponse(t, second)
	if view2.TurnID != view.TurnID {
		t.Fatalf("idempotent create returned %q, want %q", view2.TurnID, view.TurnID)
	}
	statusPath := base + "/turns/" + view.TurnID
	status := waitForTurnStatus(t, te, statusPath, cookie)
	if status.Status != "succeeded" {
		t.Fatalf("turn status = %q, want succeeded", status.Status)
	}
	msgs, err := te.chats.Messages(testCtx, c.ID)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("messages = %d (%v), want one user and one assistant", len(msgs), err)
	}
	if msgs[0].Content != "oi" || msgs[1].Content != "resposta persistida" {
		t.Fatalf("unexpected messages: %+v", msgs)
	}
}

func TestChatTurn_FailureCanBeRetriedWithoutDuplicatingQuestion(t *testing.T) {
	te := newTestEnv(t)
	provider := fakeOpenAIFirstFailureThenSuccess("agora funcionou")
	defer provider.Close()
	cookie := te.register(t, "retry-turn@test.com", "hunter2!")
	te.grantQuota(t, cookie, 100000)
	loc := te.createStudyAt(t, "tema", provider.URL, cookie)
	te.req(t, "GET", loc, "", cookie)
	sid := fid64(t, loc)
	c := te.firstChat(t, sid)
	base := loc + "/chats/" + strconv.FormatInt(c.ID, 10)
	first := te.reqCT(t, "POST", base+"/turns", "application/json", `{"message":"tente","client_key":"retry-key","web":false}`, cookie)
	view := chatTurnViewFromResponse(t, first)
	status := waitForTurnStatus(t, te, base+"/turns/"+view.TurnID, cookie)
	if status.Status != "failed" || status.ErrorCode != "provider_error" {
		t.Fatalf("failed turn = %+v", status)
	}
	retry := te.req(t, "POST", base+"/turns/"+view.TurnID+"/retry", "", cookie)
	if retry.Code != http.StatusAccepted {
		t.Fatalf("retry: expected 202, got %d body=%s", retry.Code, retry.Body.String())
	}
	status = waitForTurnStatus(t, te, base+"/turns/"+view.TurnID, cookie)
	if status.Status != "succeeded" {
		t.Fatalf("retried turn = %+v", status)
	}
	msgs, err := te.chats.Messages(testCtx, c.ID)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("messages after retry = %d (%v), want 2", len(msgs), err)
	}
	if strings.Count(msgs[0].Content, "tente") != 1 || msgs[1].Content != "agora funcionou" {
		t.Fatalf("unexpected retry messages: %+v", msgs)
	}
}
