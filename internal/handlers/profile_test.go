package handlers

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"learnix/internal/db"
)

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			img.Set(x, y, color.RGBA{R: 240, G: 180, B: 41, A: 255})
		}
	}
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func profileUpdateRequest(t *testing.T, te *testEnv, slug string, cookie *http.Cookie, includeImage bool, visible map[string]bool) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	_ = form.WriteField("csrf", te.csrfToken(t, cookie))
	_ = form.WriteField("display_name", "Pessoa Pública")
	_ = form.WriteField("bio", "Bio que deve ser controlável.")
	for _, field := range db.ProfileVisibilityFields() {
		if visible[field.Key] {
			_ = form.WriteField("visible_"+field.Key, "on")
		}
	}
	if includeImage {
		part, err := form.CreateFormFile("avatar", "avatar.png")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(tinyPNG(t)); err != nil {
			t.Fatal(err)
		}
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/profile/"+url.PathEscape(slug), &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	te.router.ServeHTTP(rr, req)
	return rr
}

func TestProfile_PublicPageOwnerRedirectAndPrivacy(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "profile-owner@test.com", "hunter2!")
	uid := uidFromCookie(t, te, cookie)
	profile, err := te.profiles.ByUser(testCtx, uid)
	if err != nil || profile == nil {
		t.Fatalf("profile = %+v, err=%v", profile, err)
	}
	visible := map[string]bool{}
	for _, field := range db.ProfileVisibilityFields() {
		visible[field.Key] = true
	}
	visible[db.ProfileVisibilityBio] = false
	visible[db.ProfileVisibilityTokensUsed] = false
	if rr := profileUpdateRequest(t, te, profile.Slug, cookie, true, visible); rr.Code != http.StatusSeeOther {
		t.Fatalf("profile update: %d %s", rr.Code, rr.Body.String())
	}

	public := te.req(t, http.MethodGet, "/profile/"+url.PathEscape(profile.Slug), "")
	if public.Code != http.StatusOK {
		t.Fatalf("public profile: %d %s", public.Code, public.Body.String())
	}
	body := public.Body.String()
	if !strings.Contains(body, "Pessoa Pública") || strings.Contains(body, "profile-owner@test.com") {
		t.Fatalf("public identity leaked or missing: %s", body)
	}
	if strings.Contains(body, "Bio que deve ser controlável.") || strings.Contains(body, "Tokens usados") {
		t.Fatalf("hidden profile fields rendered: %s", body)
	}
	if !strings.Contains(body, "/profile/"+url.PathEscape(profile.Slug)+"/avatar") {
		t.Fatalf("public avatar missing: %s", body)
	}

	avatar := te.req(t, http.MethodGet, "/profile/"+url.PathEscape(profile.Slug)+"/avatar", "")
	if avatar.Code != http.StatusOK || avatar.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("public avatar: %d %s", avatar.Code, avatar.Header().Get("Content-Type"))
	}
	owner := te.req(t, http.MethodGet, "/profile/"+url.PathEscape(profile.Slug), "", cookie)
	if owner.Code != http.StatusOK || !strings.Contains(owner.Body.String(), "Editar perfil") || !strings.Contains(owner.Body.String(), "Bio que deve ser controlável.") {
		t.Fatalf("owner profile: %d %s", owner.Code, owner.Body.String())
	}
	me := te.req(t, http.MethodGet, "/profile/me", "", cookie)
	if me.Code != http.StatusFound || me.Header().Get("Location") != "/profile/"+url.PathEscape(profile.Slug) {
		t.Fatalf("profile me: %d %s", me.Code, me.Header().Get("Location"))
	}
	visible[db.ProfileVisibilityAvatar] = false
	if rr := profileUpdateRequest(t, te, profile.Slug, cookie, false, visible); rr.Code != http.StatusSeeOther {
		t.Fatalf("hide avatar update: %d %s", rr.Code, rr.Body.String())
	}
	hiddenAvatar := te.req(t, http.MethodGet, "/profile/"+url.PathEscape(profile.Slug)+"/avatar", "")
	if hiddenAvatar.Code != http.StatusNotFound {
		t.Fatalf("hidden public avatar: %d", hiddenAvatar.Code)
	}
	ownerAvatar := te.req(t, http.MethodGet, "/profile/"+url.PathEscape(profile.Slug)+"/avatar", "", cookie)
	if ownerAvatar.Code != http.StatusOK || ownerAvatar.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("hidden owner avatar: %d cache=%q", ownerAvatar.Code, ownerAvatar.Header().Get("Cache-Control"))
	}
	guestMe := te.req(t, http.MethodGet, "/profile/me", "")
	if guestMe.Code != http.StatusFound || guestMe.Header().Get("Location") != "/login" {
		t.Fatalf("guest profile me: %d %s", guestMe.Code, guestMe.Header().Get("Location"))
	}
}

func TestProfile_UpdateRequiresOwnerAndCSRF(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "profile-a@test.com", "hunter2!")
	other := te.register(t, "profile-b@test.com", "hunter2!")
	uid := uidFromCookie(t, te, cookie)
	profile, _ := te.profiles.ByUser(testCtx, uid)

	badCSRF := profileUpdateRequestWithCSRF(t, te, profile.Slug, cookie, "invalid")
	if badCSRF.Code != http.StatusBadRequest {
		t.Fatalf("invalid csrf: %d", badCSRF.Code)
	}
	otherReq := profileUpdateRequest(t, te, profile.Slug, other, false, map[string]bool{})
	if otherReq.Code != http.StatusNotFound {
		t.Fatalf("cross-owner update: %d", otherReq.Code)
	}
}

func profileUpdateRequestWithCSRF(t *testing.T, te *testEnv, slug string, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	_ = form.WriteField("csrf", csrf)
	_ = form.WriteField("display_name", "Nome")
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/profile/"+url.PathEscape(slug), &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	te.router.ServeHTTP(rr, req)
	return rr
}

func TestProfileImageValidation(t *testing.T) {
	data := tinyPNG(t)
	mime, err := validateProfileImage(data)
	if err != nil || mime != "image/png" {
		t.Fatalf("png validation = %q, %v", mime, err)
	}
	if _, err := validateProfileImage([]byte("not an image")); err == nil {
		t.Fatal("invalid image accepted")
	}
	if _, err := validateProfileImage([]byte("RIFFxxxxWEBPVP8X")); err == nil {
		t.Fatal("malformed webp accepted")
	}
}

func TestLeaderboardIncludesPublicHandleRankAndAvatar(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "leaderboard-owner@test.com", "hunter2!")
	uid := uidFromCookie(t, te, cookie)
	profile, err := te.profiles.ByUser(testCtx, uid)
	if err != nil || profile == nil {
		t.Fatal(err)
	}
	visible := map[string]bool{}
	for _, field := range db.ProfileVisibilityFields() {
		visible[field.Key] = true
	}
	if rr := profileUpdateRequest(t, te, profile.Slug, cookie, true, visible); rr.Code != http.StatusSeeOther {
		t.Fatalf("profile update: %d %s", rr.Code, rr.Body.String())
	}
	for i := int64(1); i <= 5; i++ {
		if err := te.leaderboard.Record(testCtx, db.RankedResult{
			QuizID: i, UserID: uid, Topic: "rank", Preset: "moderate", Total: 10,
			Correct: 8, ScoreCents: 800, WeightCents: 100, WeightedScoreCents: 800,
			FinishedAt: time.Now().UTC().Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}

	response := te.req(t, http.MethodGet, "/leaderboard/nota?period=all", "")
	if response.Code != http.StatusOK {
		t.Fatalf("leaderboard status: %d %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, expected := range []string{`"username":"user"`, `"tag":"0001"`, `"rank_label":"Ouro"`, `/profile/user%230001/avatar`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("leaderboard identity missing %q: %s", expected, body)
		}
	}
	if strings.Contains(body, "leaderboard-owner@test.com") {
		t.Fatal("leaderboard leaked email")
	}
}
