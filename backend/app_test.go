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
	link string
}

func (m *captureMailer) SendMagicLink(_ context.Context, _, link string) error {
	m.link = link
	return nil
}

type failingMailer struct{}

func (failingMailer) SendMagicLink(context.Context, string, string) error {
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
		BaseURL:   "http://example.com",
		StaticDir: filepath.Join("..", "frontend"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return app, mailer
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
		t.Fatalf("request magic link: got %d: %s", response.Code, response.Body.String())
	}
	if mailer.link == "" {
		t.Fatal("magic link was not sent")
	}

	magicURL, err := url.Parse(mailer.link)
	if err != nil {
		t.Fatal(err)
	}
	response = requestJSON(t, app.Handler(), http.MethodGet, magicURL.RequestURI(), nil, nil)
	if response.Code != http.StatusOK || len(response.Result().Cookies()) != 0 {
		t.Fatalf("magic link confirmation: got %d with cookies %#v", response.Code, response.Result().Cookies())
	}
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("magic confirmation security headers: %#v", response.Header())
	}

	form := url.Values{"token": {magicURL.Query().Get("token")}}
	request := httptest.NewRequest(http.MethodPost, "/api/auth/verify", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response = httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("verify magic link: got %d: %s", response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("unexpected session cookie: %#v", cookies)
	}
	return cookies[0]
}

func TestMagicLinkIsSingleUse(t *testing.T) {
	app, mailer := newTestApp(t)
	_ = authenticate(t, app, mailer)

	magicURL, _ := url.Parse(mailer.link)
	form := url.Values{"token": {magicURL.Query().Get("token")}}
	request := httptest.NewRequest(http.MethodPost, "/api/auth/verify", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("reused magic link: got %d, want %d", response.Code, http.StatusUnauthorized)
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
	if response.Code != http.StatusAccepted || mailer.link == "" {
		t.Fatalf("retry after failed send: got %d, link %q", response.Code, mailer.link)
	}
}

func TestFailedReplacementPreservesPreviousMagicLink(t *testing.T) {
	app, mailer := newTestApp(t)
	response := requestJSON(t, app.Handler(), http.MethodPost, "/api/auth/request", map[string]string{"email": "kid@example.com"}, nil)
	if response.Code != http.StatusAccepted {
		t.Fatalf("first magic link: got %d", response.Code)
	}
	firstLink := mailer.link
	if _, err := app.store.db.Exec(`UPDATE magic_tokens SET created_at = created_at - 120`); err != nil {
		t.Fatal(err)
	}
	app.mailer = failingMailer{}
	response = requestJSON(t, app.Handler(), http.MethodPost, "/api/auth/request", map[string]string{"email": "kid@example.com"}, nil)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("failed replacement: got %d", response.Code)
	}

	magicURL, _ := url.Parse(firstLink)
	form := url.Values{"token": {magicURL.Query().Get("token")}}
	request := httptest.NewRequest(http.MethodPost, "/api/auth/verify", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response = httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("previous link after failed replacement: got %d: %s", response.Code, response.Body.String())
	}
}

func TestInvalidEmailDoesNotCreateMagicLink(t *testing.T) {
	app, mailer := newTestApp(t)
	response := requestJSON(t, app.Handler(), http.MethodPost, "/api/auth/request", map[string]string{"email": "not-an-email"}, nil)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid email: got %d, want %d", response.Code, http.StatusBadRequest)
	}
	if mailer.link != "" {
		t.Fatal("invalid email sent a magic link")
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
	err = (SMTPMailer{Host: host, Port: port, From: "test@example.com"}).SendMagicLink(ctx, "kid@example.com", "http://example.com/magic")
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
	err = (SMTPMailer{Host: host, Port: port, From: "test@example.com"}).SendMagicLink(context.Background(), "kid@example.com", "http://example.com/magic")
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("SMTP without STARTTLS returned %v", err)
	}
}

func TestMagicLinkRateLimitSpansDifferentEmails(t *testing.T) {
	app, mailer := newTestApp(t)
	for index := 0; index < app.loginLimiter.perIPLimit; index++ {
		response := requestJSON(t, app.Handler(), http.MethodPost, "/api/auth/request", map[string]string{
			"email": fmt.Sprintf("kid%d@example.com", index),
		}, nil)
		if response.Code != http.StatusAccepted {
			t.Fatalf("request %d: got %d", index, response.Code)
		}
		mailer.link = ""
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
	app, err := NewApp(store, &captureMailer{}, Config{BaseURL: "http://example.com", StaticDir: staticDir})
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
		if _, err := NewApp(store, &captureMailer{}, Config{BaseURL: "http://example.com", StaticDir: invalidPath}); err == nil || !strings.Contains(err.Error(), "STATIC_DIR") {
			t.Fatalf("invalid static directory %q returned %v", invalidPath, err)
		}
	}
}
