package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type captureMailer struct {
	code string
}

func (m *captureMailer) SendLoginCode(_ context.Context, _, code string) error {
	m.code = code
	return nil
}

type failingMailer struct{}

func (failingMailer) SendLoginCode(context.Context, string, string) error {
	return errors.New("mail server unavailable")
}

func newTestApp(t *testing.T) (*App, *captureMailer) {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	mailer := &captureMailer{}
	app, err := NewApp(store, mailer, Config{
		BaseURL:         "http://example.com",
		LoginCodeSecret: "test-login-code-secret",
		StaticDir:       filepath.Join("..", "frontend"),
		AdminEmails:     []string{"admin@example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return app, mailer
}

func TestAdminStatisticsRequireAdminAndReportActivity(t *testing.T) {
	app, mailer := newTestApp(t)
	userCookie := authenticate(t, app, mailer)
	response := requestJSON(t, app.Handler(), http.MethodPost, "/api/activity", nil, userCookie)
	if response.Code != http.StatusNoContent {
		t.Fatalf("record user activity: got %d: %s", response.Code, response.Body.String())
	}

	response = requestJSON(t, app.Handler(), http.MethodGet, "/api/admin/stats", nil, nil)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated admin stats: got %d, want %d", response.Code, http.StatusUnauthorized)
	}
	response = requestJSON(t, app.Handler(), http.MethodGet, "/api/admin/stats", nil, userCookie)
	if response.Code != http.StatusForbidden {
		t.Fatalf("non-admin stats: got %d, want %d", response.Code, http.StatusForbidden)
	}

	adminMailer := &captureMailer{}
	app.mailer = adminMailer
	adminCookie := authenticateAs(t, app, adminMailer, "admin@example.com")
	response = requestJSON(t, app.Handler(), http.MethodPost, "/api/activity", nil, adminCookie)
	if response.Code != http.StatusNoContent {
		t.Fatalf("record admin activity: got %d: %s", response.Code, response.Body.String())
	}
	response = requestJSON(t, app.Handler(), http.MethodPost, "/api/pages", map[string]string{
		"title": "Admin page", "content": "<h1>Stats</h1>",
	}, adminCookie)
	if response.Code != http.StatusCreated {
		t.Fatalf("create admin page: got %d: %s", response.Code, response.Body.String())
	}

	response = requestJSON(t, app.Handler(), http.MethodGet, "/api/admin/stats", nil, adminCookie)
	if response.Code != http.StatusOK {
		t.Fatalf("admin stats: got %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		Summary struct {
			TotalUsers  int `json:"totalUsers"`
			TotalPages  int `json:"totalPages"`
			OnlineUsers int `json:"onlineUsers"`
		} `json:"summary"`
		Activity []struct {
			Date         string `json:"date"`
			NewUsers     int    `json:"newUsers"`
			PagesCreated int    `json:"pagesCreated"`
			ActiveUsers  int    `json:"activeUsers"`
		} `json:"activity"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Summary.TotalUsers != 2 || payload.Summary.TotalPages != 1 || payload.Summary.OnlineUsers != 2 {
		t.Fatalf("unexpected summary: %#v", payload.Summary)
	}
	if len(payload.Activity) != 30 {
		t.Fatalf("activity days = %d, want 30", len(payload.Activity))
	}
}

func TestAdminStatsUsesUTCDateAndOnlineBoundaries(t *testing.T) {
	app, _ := newTestApp(t)
	now := time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC)
	for index, lastSeen := range []time.Time{now.Add(-5 * time.Minute), now.Add(-5*time.Minute - time.Second)} {
		result, err := app.store.db.Exec(`INSERT INTO users (email, created_at) VALUES (?, ?)`, fmt.Sprintf("boundary%d@example.com", index), now.AddDate(0, 0, -29).Unix())
		if err != nil {
			t.Fatal(err)
		}
		userID, err := result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		if err := app.store.TouchActivity(context.Background(), userID, lastSeen); err != nil {
			t.Fatal(err)
		}
	}

	stats, err := app.store.AdminStats(context.Background(), now, 30, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Summary.OnlineUsers != 1 {
		t.Fatalf("online users = %d, want 1", stats.Summary.OnlineUsers)
	}
	if len(stats.Activity) != 30 || stats.Activity[0].Date != "2026-08-26" || stats.Activity[29].Date != "2026-09-24" {
		t.Fatalf("unexpected activity date range: %#v", stats.Activity)
	}
	if stats.Activity[0].NewUsers != 2 {
		t.Fatalf("first-day new users = %d, want 2", stats.Activity[0].NewUsers)
	}
}

func TestCurrentUserIncludesAdminStatus(t *testing.T) {
	app, mailer := newTestApp(t)
	cookie := authenticateAs(t, app, mailer, "admin@example.com")
	response := requestJSON(t, app.Handler(), http.MethodGet, "/api/auth/me", nil, cookie)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"isAdmin":true`) {
		t.Fatalf("admin current user: got %d: %s", response.Code, response.Body.String())
	}
}

func TestAdminPagesRequireAdmin(t *testing.T) {
	app, mailer := newTestApp(t)
	userCookie := authenticate(t, app, mailer)

	adminMailer := &captureMailer{}
	app.mailer = adminMailer
	adminCookie := authenticateAs(t, app, adminMailer, "admin@example.com")

	for _, path := range []string{"/admin", "/admin.html"} {
		for _, test := range []struct {
			name   string
			cookie *http.Cookie
			status int
		}{
			{name: "unauthenticated", status: http.StatusUnauthorized},
			{name: "non-admin", cookie: userCookie, status: http.StatusForbidden},
			{name: "admin", cookie: adminCookie, status: http.StatusOK},
		} {
			t.Run(path+"/"+test.name, func(t *testing.T) {
				response := requestJSON(t, app.Handler(), http.MethodGet, path, nil, test.cookie)
				if response.Code != test.status {
					t.Fatalf("GET %s: got %d, want %d", path, response.Code, test.status)
				}
			})
		}
	}
}

func TestNewAppRejectsInvalidAdminEmail(t *testing.T) {
	app, mailer := newTestApp(t)
	_, err := NewApp(app.store, mailer, Config{
		BaseURL:         "http://example.com",
		LoginCodeSecret: "test-login-code-secret",
		StaticDir:       filepath.Join("..", "frontend"),
		AdminEmails:     []string{"admin@example.com", "not-an-email"},
	})
	if err == nil || !strings.Contains(err.Error(), "ADMIN_EMAILS") || !strings.Contains(err.Error(), "not-an-email") {
		t.Fatalf("invalid admin email error = %v", err)
	}
}

func TestActivityFailureOnlyFailsActivityEndpoint(t *testing.T) {
	app, mailer := newTestApp(t)
	cookie := authenticate(t, app, mailer)
	if _, err := app.store.db.Exec(`DROP TABLE user_activity_days`); err != nil {
		t.Fatal(err)
	}

	response := requestJSON(t, app.Handler(), http.MethodGet, "/api/pages", nil, cookie)
	if response.Code != http.StatusOK {
		t.Fatalf("list pages with unavailable activity storage: got %d: %s", response.Code, response.Body.String())
	}

	response = requestJSON(t, app.Handler(), http.MethodPost, "/api/activity", nil, cookie)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("record activity with unavailable activity storage: got %d, want %d", response.Code, http.StatusInternalServerError)
	}
}

func requestJSON(t *testing.T, handler http.Handler, method, target string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var payload bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&payload).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, target, &payload)
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	return response
}

func authenticate(t *testing.T, app *App, mailer *captureMailer) *http.Cookie {
	return authenticateAs(t, app, mailer, "kid@example.com")
}

func authenticateAs(t *testing.T, app *App, mailer *captureMailer, email string) *http.Cookie {
	t.Helper()
	response := requestJSON(t, app.Handler(), http.MethodPost, "/api/auth/request", map[string]string{"email": email}, nil)
	if response.Code != http.StatusAccepted {
		t.Fatalf("request login code: got %d: %s", response.Code, response.Body.String())
	}
	if len(mailer.code) != 6 {
		t.Fatalf("login code = %q, want six digits", mailer.code)
	}
	if _, err := strconv.Atoi(mailer.code); err != nil {
		t.Fatalf("login code = %q, want six digits", mailer.code)
	}

	response = requestJSON(t, app.Handler(), http.MethodPost, "/api/auth/verify", map[string]string{"email": email, "code": mailer.code}, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("verify login code: got %d: %s", response.Code, response.Body.String())
	}
	var user User
	if err := json.NewDecoder(response.Body).Decode(&user); err != nil || user.Email != email {
		t.Fatalf("verified user: got %#v, decode error %v", user, err)
	}
	if user.IsAdmin != (email == "admin@example.com") {
		t.Fatalf("verified user admin status = %t for %s", user.IsAdmin, email)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("unexpected session cookie: %#v", cookies)
	}
	return cookies[0]
}

func TestLoginCodeIsSingleUse(t *testing.T) {
	app, mailer := newTestApp(t)
	_ = authenticate(t, app, mailer)

	response := requestJSON(t, app.Handler(), http.MethodPost, "/api/auth/verify", map[string]string{"email": "kid@example.com", "code": mailer.code}, nil)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("reused login code: got %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestOutstandingMagicLinkRemainsValidDuringMigration(t *testing.T) {
	app, _ := newTestApp(t)
	const token = "legacy-high-entropy-token"
	now := time.Now().Unix()
	result, err := app.store.db.Exec(`INSERT INTO users (email, created_at) VALUES (?, ?)`, "kid@example.com", now)
	if err != nil {
		t.Fatal(err)
	}
	userID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.db.Exec(`INSERT INTO magic_tokens (token_hash, user_id, expires_at, created_at) VALUES (?, ?, ?, ?)`, hashToken(token), userID, now+60, now); err != nil {
		t.Fatal(err)
	}

	response := requestJSON(t, app.Handler(), http.MethodGet, "/api/auth/verify?token="+url.QueryEscape(token), nil, nil)
	if response.Code != http.StatusOK || len(response.Result().Cookies()) != 0 {
		t.Fatalf("legacy link confirmation: got %d with cookies %#v", response.Code, response.Result().Cookies())
	}

	form := url.Values{"token": {token}}
	request := httptest.NewRequest(http.MethodPost, "/api/auth/verify", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response = httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || len(response.Result().Cookies()) != 1 {
		t.Fatalf("legacy link verification: got %d with cookies %#v", response.Code, response.Result().Cookies())
	}
}

func TestLoginCodeRequiresMatchingEmail(t *testing.T) {
	app, mailer := newTestApp(t)
	response := requestJSON(t, app.Handler(), http.MethodPost, "/api/auth/request", map[string]string{"email": "kid@example.com"}, nil)
	if response.Code != http.StatusAccepted {
		t.Fatalf("request login code: got %d: %s", response.Code, response.Body.String())
	}

	response = requestJSON(t, app.Handler(), http.MethodPost, "/api/auth/verify", map[string]string{
		"email": "someone-else@example.com",
		"code":  mailer.code,
	}, nil)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("code with wrong email: got %d, want %d", response.Code, http.StatusUnauthorized)
	}

	response = requestJSON(t, app.Handler(), http.MethodPost, "/api/auth/verify", map[string]string{
		"email": "kid@example.com",
		"code":  mailer.code,
	}, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("code after wrong email attempt: got %d: %s", response.Code, response.Body.String())
	}
}

func TestLoginCodeStoredWithServerSecret(t *testing.T) {
	app, mailer := newTestApp(t)
	response := requestJSON(t, app.Handler(), http.MethodPost, "/api/auth/request", map[string]string{"email": "kid@example.com"}, nil)
	if response.Code != http.StatusAccepted {
		t.Fatalf("request login code: got %d: %s", response.Code, response.Body.String())
	}

	var storedHash string
	if err := app.store.db.QueryRow(`SELECT code_hash FROM login_codes WHERE active = 1`).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if storedHash != hashLoginCode(app.loginCodeSecret, "kid@example.com", mailer.code) {
		t.Fatal("stored login code hash does not use the application secret")
	}
	if storedHash == hashToken("kid@example.com\x00"+mailer.code) {
		t.Fatal("stored login code uses an unkeyed hash")
	}
}

func TestRevokingCollidingReplacementPreservesActiveCode(t *testing.T) {
	app, _ := newTestApp(t)
	const email = "kid@example.com"
	codeHash := hashLoginCode(app.loginCodeSecret, email, "123456")
	firstID, err := app.store.CreateLoginCode(context.Background(), email, codeHash, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.ActivateLoginCode(context.Background(), firstID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.db.Exec(`UPDATE login_codes SET created_at = created_at - 120 WHERE id = ?`, firstID); err != nil {
		t.Fatal(err)
	}
	secondID, err := app.store.CreateLoginCode(context.Background(), email, codeHash, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.RevokeLoginCode(context.Background(), secondID); err != nil {
		t.Fatal(err)
	}

	if _, err := app.store.ConsumeLoginCode(context.Background(), email, codeHash, hashToken("session"), time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("active code after colliding replacement failed: %v", err)
	}
}

func TestLoginCodeVerificationIsRateLimited(t *testing.T) {
	app, mailer := newTestApp(t)
	response := requestJSON(t, app.Handler(), http.MethodPost, "/api/auth/request", map[string]string{"email": "kid@example.com"}, nil)
	if response.Code != http.StatusAccepted {
		t.Fatalf("request login code: got %d: %s", response.Code, response.Body.String())
	}

	wrongCode := "000000"
	if mailer.code == wrongCode {
		wrongCode = "000001"
	}
	for attempt := 0; attempt < app.loginVerifyLimiter.perIPLimit; attempt++ {
		response = requestJSON(t, app.Handler(), http.MethodPost, "/api/auth/verify", map[string]string{
			"email": "kid@example.com",
			"code":  wrongCode,
		}, nil)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("verification attempt %d: got %d, want %d", attempt, response.Code, http.StatusUnauthorized)
		}
	}

	response = requestJSON(t, app.Handler(), http.MethodPost, "/api/auth/verify", map[string]string{
		"email": "kid@example.com",
		"code":  wrongCode,
	}, nil)
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("rate-limited verification: got %d, want %d", response.Code, http.StatusTooManyRequests)
	}
}

func TestLoginCodeExpiresAfterFailedAttempts(t *testing.T) {
	app, mailer := newTestApp(t)
	response := requestJSON(t, app.Handler(), http.MethodPost, "/api/auth/request", map[string]string{"email": "kid@example.com"}, nil)
	if response.Code != http.StatusAccepted {
		t.Fatalf("request login code: got %d: %s", response.Code, response.Body.String())
	}

	wrongCode := "000000"
	if mailer.code == wrongCode {
		wrongCode = "000001"
	}
	for attempt := 0; attempt < maxLoginCodeAttempts; attempt++ {
		response = requestJSON(t, app.Handler(), http.MethodPost, "/api/auth/verify", map[string]string{
			"email": "kid@example.com",
			"code":  wrongCode,
		}, nil)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("failed attempt %d: got %d, want %d", attempt, response.Code, http.StatusUnauthorized)
		}
	}

	response = requestJSON(t, app.Handler(), http.MethodPost, "/api/auth/verify", map[string]string{
		"email": "kid@example.com",
		"code":  mailer.code,
	}, nil)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("correct code after failed attempts: got %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestExpiredLoginCodeIsRejected(t *testing.T) {
	app, mailer := newTestApp(t)
	response := requestJSON(t, app.Handler(), http.MethodPost, "/api/auth/request", map[string]string{"email": "kid@example.com"}, nil)
	if response.Code != http.StatusAccepted {
		t.Fatalf("request login code: got %d: %s", response.Code, response.Body.String())
	}
	if _, err := app.store.db.Exec(`UPDATE login_codes SET expires_at = 0`); err != nil {
		t.Fatal(err)
	}

	response = requestJSON(t, app.Handler(), http.MethodPost, "/api/auth/verify", map[string]string{
		"email": "kid@example.com",
		"code":  mailer.code,
	}, nil)
	if response.Code != http.StatusUnauthorized || len(response.Result().Cookies()) != 0 {
		t.Fatalf("expired code: got %d with cookies %#v", response.Code, response.Result().Cookies())
	}
}

func TestPageLifecycleRequiresAuthenticationAndOwnership(t *testing.T) {
	app, mailer := newTestApp(t)

	response := requestJSON(t, app.Handler(), http.MethodGet, "/api/pages", nil, nil)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list: got %d, want %d", response.Code, http.StatusUnauthorized)
	}

	cookie := authenticate(t, app, mailer)
	response = requestJSON(t, app.Handler(), http.MethodPost, "/api/pages", map[string]string{
		"title":   "My first page",
		"content": "<h1>Hello!</h1>",
	}, cookie)
	if response.Code != http.StatusCreated {
		t.Fatalf("create page: got %d: %s", response.Code, response.Body.String())
	}
	var page Page
	if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if page.ID == 0 || page.Title != "My first page" {
		t.Fatalf("unexpected page: %#v", page)
	}

	secondMailer := &captureMailer{}
	app.mailer = secondMailer
	secondCookie := authenticateAs(t, app, secondMailer, "someone-else@example.com")
	pagePath := "/api/pages/" + strconv.FormatInt(page.ID, 10)
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		var body any
		if method == http.MethodPut {
			body = map[string]string{"title": "Not mine", "content": "<p>Nope</p>"}
		}
		response = requestJSON(t, app.Handler(), method, pagePath, body, secondCookie)
		if response.Code != http.StatusNotFound {
			t.Fatalf("second user %s page: got %d, want %d", method, response.Code, http.StatusNotFound)
		}
	}

	response = requestJSON(t, app.Handler(), http.MethodPut, "/api/pages/"+strconv.FormatInt(page.ID, 10), map[string]string{
		"title":   "Updated page",
		"content": "<h1>Updated!</h1>",
	}, cookie)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Updated page") {
		t.Fatalf("update page: got %d: %s", response.Code, response.Body.String())
	}

	response = requestJSON(t, app.Handler(), http.MethodGet, "/api/pages", nil, cookie)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Updated page") {
		t.Fatalf("list pages: got %d: %s", response.Code, response.Body.String())
	}

	response = requestJSON(t, app.Handler(), http.MethodDelete, "/api/pages/"+strconv.FormatInt(page.ID, 10), nil, cookie)
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete page: got %d: %s", response.Code, response.Body.String())
	}
}

func TestEmptyPageContentRoundTrips(t *testing.T) {
	app, mailer := newTestApp(t)
	cookie := authenticate(t, app, mailer)
	response := requestJSON(t, app.Handler(), http.MethodPost, "/api/pages", map[string]string{"title": "Blank", "content": ""}, cookie)
	if response.Code != http.StatusCreated {
		t.Fatalf("create blank page: got %d: %s", response.Code, response.Body.String())
	}
	var payload map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	content, exists := payload["content"]
	if !exists || content != "" {
		t.Fatalf("blank content was not preserved: %#v", payload)
	}
}

func TestFailedEmailDoesNotRateLimitRetry(t *testing.T) {
	app, _ := newTestApp(t)
	app.mailer = failingMailer{}
	response := requestJSON(t, app.Handler(), http.MethodPost, "/api/auth/request", map[string]string{"email": "kid@example.com"}, nil)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("failed mail send: got %d, want %d", response.Code, http.StatusBadGateway)
	}

	mailer := &captureMailer{}
	app.mailer = mailer
	response = requestJSON(t, app.Handler(), http.MethodPost, "/api/auth/request", map[string]string{"email": "kid@example.com"}, nil)
	if response.Code != http.StatusAccepted || mailer.code == "" {
		t.Fatalf("retry after failed send: got %d, code %q", response.Code, mailer.code)
	}
}

func TestFailedReplacementPreservesPreviousLoginCode(t *testing.T) {
	app, mailer := newTestApp(t)
	response := requestJSON(t, app.Handler(), http.MethodPost, "/api/auth/request", map[string]string{"email": "kid@example.com"}, nil)
	if response.Code != http.StatusAccepted {
		t.Fatalf("first login code: got %d", response.Code)
	}
	firstCode := mailer.code
	if _, err := app.store.db.Exec(`UPDATE login_codes SET created_at = created_at - 120`); err != nil {
		t.Fatal(err)
	}
	app.mailer = failingMailer{}
	response = requestJSON(t, app.Handler(), http.MethodPost, "/api/auth/request", map[string]string{"email": "kid@example.com"}, nil)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("failed replacement: got %d", response.Code)
	}

	response = requestJSON(t, app.Handler(), http.MethodPost, "/api/auth/verify", map[string]string{"email": "kid@example.com", "code": firstCode}, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("previous code after failed replacement: got %d: %s", response.Code, response.Body.String())
	}
}

func TestSuccessfulReplacementInvalidatesPreviousLoginCode(t *testing.T) {
	app, mailer := newTestApp(t)
	response := requestJSON(t, app.Handler(), http.MethodPost, "/api/auth/request", map[string]string{"email": "kid@example.com"}, nil)
	if response.Code != http.StatusAccepted {
		t.Fatalf("first login code: got %d", response.Code)
	}
	firstCode := mailer.code
	if _, err := app.store.db.Exec(`UPDATE login_codes SET created_at = created_at - 120`); err != nil {
		t.Fatal(err)
	}

	response = requestJSON(t, app.Handler(), http.MethodPost, "/api/auth/request", map[string]string{"email": "kid@example.com"}, nil)
	if response.Code != http.StatusAccepted {
		t.Fatalf("replacement login code: got %d", response.Code)
	}
	if firstCode == mailer.code {
		t.Skip("random generator returned the same code twice")
	}

	response = requestJSON(t, app.Handler(), http.MethodPost, "/api/auth/verify", map[string]string{"email": "kid@example.com", "code": firstCode}, nil)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("superseded code: got %d, want %d", response.Code, http.StatusUnauthorized)
	}
	response = requestJSON(t, app.Handler(), http.MethodPost, "/api/auth/verify", map[string]string{"email": "kid@example.com", "code": mailer.code}, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("replacement code: got %d: %s", response.Code, response.Body.String())
	}
}

func TestInvalidEmailDoesNotCreateLoginCode(t *testing.T) {
	app, mailer := newTestApp(t)
	response := requestJSON(t, app.Handler(), http.MethodPost, "/api/auth/request", map[string]string{"email": "not-an-email"}, nil)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid email: got %d, want %d", response.Code, http.StatusBadRequest)
	}
	if mailer.code != "" {
		t.Fatal("invalid email sent a login code")
	}
}

func TestSMTPMailerHonorsContextCancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	connections := make(chan net.Conn, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			connections <- connection
		}
	}()

	host, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = (SMTPMailer{Host: host, Port: port, From: "test@example.com"}).SendLoginCode(ctx, "kid@example.com", "123456")
	if err == nil {
		t.Fatal("stalled SMTP send unexpectedly succeeded")
	}
	if time.Since(started) > time.Second {
		t.Fatalf("SMTP cancellation took too long: %s", time.Since(started))
	}
	select {
	case connection := <-connections:
		_ = connection.Close()
	default:
	}
}

func TestSMTPMailerRequiresSTARTTLS(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		reader := bufio.NewReader(connection)
		_, _ = connection.Write([]byte("220 test SMTP\r\n"))
		_, _ = reader.ReadString('\n')
		_, _ = connection.Write([]byte("250 test\r\n"))
	}()

	host, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	err = (SMTPMailer{Host: host, Port: port, From: "test@example.com"}).SendLoginCode(context.Background(), "kid@example.com", "123456")
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("SMTP without STARTTLS returned %v", err)
	}
}

func TestLoginCodeRateLimitSpansDifferentEmails(t *testing.T) {
	app, mailer := newTestApp(t)
	for index := 0; index < app.loginRequestLimiter.perIPLimit; index++ {
		response := requestJSON(t, app.Handler(), http.MethodPost, "/api/auth/request", map[string]string{
			"email": fmt.Sprintf("kid%d@example.com", index),
		}, nil)
		if response.Code != http.StatusAccepted {
			t.Fatalf("request %d: got %d", index, response.Code)
		}
		mailer.code = ""
	}
	response := requestJSON(t, app.Handler(), http.MethodPost, "/api/auth/request", map[string]string{"email": "blocked@example.com"}, nil)
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("rate-limited request: got %d, want %d", response.Code, http.StatusTooManyRequests)
	}
}

func TestPerIPRateLimitCanBeDisabledBehindProxy(t *testing.T) {
	limiter := newRequestLimiter(-1, 100)
	for index := 0; index < 20; index++ {
		request := httptest.NewRequest(http.MethodPost, "/api/auth/request", nil)
		request.RemoteAddr = "10.0.0.1:1234"
		if !limiter.Allow(request) {
			t.Fatalf("request %d was limited with per-IP limiting disabled", index)
		}
	}
	if len(limiter.perIP) != 0 {
		t.Fatalf("disabled per-IP limiter tracked %d addresses", len(limiter.perIP))
	}
}

func TestExpiredSessionCanStillLogOut(t *testing.T) {
	app, mailer := newTestApp(t)
	cookie := authenticate(t, app, mailer)
	if _, err := app.store.db.Exec(`UPDATE sessions SET expires_at = 0`); err != nil {
		t.Fatal(err)
	}
	response := requestJSON(t, app.Handler(), http.MethodGet, "/api/pages", nil, cookie)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expired session: got %d, want %d", response.Code, http.StatusUnauthorized)
	}
	response = requestJSON(t, app.Handler(), http.MethodPost, "/api/auth/logout", nil, cookie)
	if response.Code != http.StatusNoContent {
		t.Fatalf("logout with expired session: got %d, want %d", response.Code, http.StatusNoContent)
	}
}

func TestNewAppRejectsInvalidBaseURL(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := NewApp(store, &captureMailer{}, Config{BaseURL: "javascript:alert(1)", StaticDir: filepath.Join("..", "frontend")}); err == nil || !strings.Contains(err.Error(), "BASE_URL") {
		t.Fatalf("invalid BASE_URL returned %v", err)
	}
}

func TestNewAppRequiresLoginCodeSecretOutsideLocalDevelopment(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	_, err = NewApp(store, &captureMailer{}, Config{BaseURL: "https://example.com", StaticDir: filepath.Join("..", "frontend")})
	if err == nil || !strings.Contains(err.Error(), "AUTH_CODE_SECRET") {
		t.Fatalf("missing production AUTH_CODE_SECRET returned %v", err)
	}
}

func TestStaticDirectoryValidationAndServing(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	staticDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(staticDir, "index.html"), []byte("static sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	app, err := NewApp(store, &captureMailer{}, Config{BaseURL: "http://example.com", LoginCodeSecret: "test-login-code-secret", StaticDir: staticDir})
	if err != nil {
		t.Fatal(err)
	}
	response := requestJSON(t, app.Handler(), http.MethodGet, "/", nil, nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "static sentinel") {
		t.Fatalf("static index: got %d: %s", response.Code, response.Body.String())
	}

	regularFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(regularFile, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, invalidPath := range []string{"", filepath.Join(t.TempDir(), "missing"), regularFile, t.TempDir()} {
		if _, err := NewApp(store, &captureMailer{}, Config{BaseURL: "http://example.com", LoginCodeSecret: "test-login-code-secret", StaticDir: invalidPath}); err == nil || !strings.Contains(err.Error(), "STATIC_DIR") {
			t.Fatalf("invalid static directory %q returned %v", invalidPath, err)
		}
	}
}
