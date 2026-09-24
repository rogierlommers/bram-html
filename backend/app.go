package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	defaultMagicLinkTTL   = 15 * time.Minute
	defaultSessionTTL     = 30 * 24 * time.Hour
	defaultMaxRequestBody = 1 << 20
)

var errRequestTooLarge = errors.New("request body is too large")

var magicLinkPage = template.Must(template.New("magic-link").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Sign in · bram-html</title><link rel="stylesheet" href="/styles.css"></head>
<body><main class="confirmation-page"><section class="confirmation-card"><p class="eyebrow">ONE MORE STEP</p>
<h1>Sign in to bram-html?</h1><p>This link can only be used once.</p>
<form method="post" action="/api/auth/verify"><input type="hidden" name="token" value="{{.}}">
<button class="save-button" type="submit">Yes, sign me in</button></form></section></main></body></html>`))

type Config struct {
	BaseURL          string
	CookieName       string
	MagicLinkTTL     time.Duration
	SessionTTL       time.Duration
	MaxRequestBody   int64
	LoginPerIPLimit  int
	LoginGlobalLimit int
	StaticDir        string
}

type App struct {
	store        *Store
	mailer       Mailer
	config       Config
	handler      http.Handler
	cookieSecure bool
	loginLimiter *requestLimiter
}

type userContextKey struct{}

func NewApp(store *Store, mailer Mailer, config Config) (*App, error) {
	if config.CookieName == "" {
		config.CookieName = "bram_session"
	}
	if config.MagicLinkTTL == 0 {
		config.MagicLinkTTL = defaultMagicLinkTTL
	}
	if config.SessionTTL == 0 {
		config.SessionTTL = defaultSessionTTL
	}
	if config.MaxRequestBody == 0 {
		config.MaxRequestBody = defaultMaxRequestBody
	}
	if config.LoginPerIPLimit == 0 {
		config.LoginPerIPLimit = 5
	}
	if config.LoginGlobalLimit == 0 {
		config.LoginGlobalLimit = 100
	}
	baseURL, err := validateBaseURL(config.BaseURL)
	if err != nil {
		return nil, err
	}
	if config.StaticDir == "" {
		return nil, errors.New("STATIC_DIR must point to the frontend directory")
	}
	staticInfo, err := os.Stat(config.StaticDir)
	if err != nil || !staticInfo.IsDir() {
		return nil, fmt.Errorf("STATIC_DIR must point to the frontend directory: %s", config.StaticDir)
	}
	indexFile, err := os.Open(filepath.Join(config.StaticDir, "index.html"))
	if err != nil {
		return nil, fmt.Errorf("STATIC_DIR must contain a readable index.html: %s", config.StaticDir)
	}
	indexInfo, statErr := indexFile.Stat()
	_ = indexFile.Close()
	if statErr != nil || !indexInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("STATIC_DIR must contain a readable index.html: %s", config.StaticDir)
	}
	config.BaseURL = strings.TrimRight(baseURL.String(), "/")
	app := &App{store: store, mailer: mailer, config: config, cookieSecure: baseURL.Scheme == "https", loginLimiter: newRequestLimiter(config.LoginPerIPLimit, config.LoginGlobalLimit)}
	app.handler = app.routes()
	return app, nil
}

func validateBaseURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return nil, fmt.Errorf("BASE_URL must be an absolute HTTP(S) origin, such as https://bram-html.example")
	}
	return parsed, nil
}

func (a *App) Handler() http.Handler { return a.handler }

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/auth/request", a.requestMagicLink)
	mux.HandleFunc("GET /api/auth/verify", a.confirmMagicLink)
	mux.HandleFunc("POST /api/auth/verify", a.verifyMagicLink)
	mux.HandleFunc("GET /api/auth/me", a.withUser(a.currentUser))
	mux.HandleFunc("POST /api/auth/logout", a.logout)
	mux.HandleFunc("GET /api/pages", a.withUser(a.listPages))
	mux.HandleFunc("POST /api/pages", a.withUser(a.createPage))
	mux.HandleFunc("GET /api/pages/{id}", a.withUser(a.getPage))
	mux.HandleFunc("PUT /api/pages/{id}", a.withUser(a.updatePage))
	mux.HandleFunc("DELETE /api/pages/{id}", a.withUser(a.deletePage))

	mux.Handle("/", http.FileServer(http.Dir(a.config.StaticDir)))
	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src https://fonts.gstatic.com; frame-src 'self'; script-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

func (a *App) requestMagicLink(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Email string `json:"email"`
	}
	if err := a.decodeJSON(w, r, &input); err != nil {
		writeDecodeError(w, err)
		return
	}
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	parsed, err := mail.ParseAddress(input.Email)
	if err != nil || parsed.Address != input.Email || len(input.Email) > 254 {
		writeError(w, http.StatusBadRequest, "enter a valid email address")
		return
	}
	if !a.loginLimiter.Allow(r) {
		w.Header().Set("Retry-After", "600")
		writeError(w, http.StatusTooManyRequests, "too many sign-in requests; try again later")
		return
	}

	token, err := randomToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create sign-in link")
		return
	}
	err = a.store.CreateMagicLink(r.Context(), input.Email, hashToken(token), time.Now().Add(a.config.MagicLinkTTL))
	if errors.Is(err, ErrRateLimited) {
		writeJSON(w, http.StatusAccepted, map[string]string{"message": "If that address can receive mail, a sign-in link is on its way."})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create sign-in link")
		return
	}

	link := a.config.BaseURL + "/api/auth/verify?token=" + url.QueryEscape(token)
	if err := a.mailer.SendMagicLink(r.Context(), input.Email, link); err != nil {
		_ = a.store.RevokeMagicLink(r.Context(), hashToken(token))
		writeError(w, http.StatusBadGateway, "could not send sign-in email")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"message": "If that address can receive mail, a sign-in link is on its way."})
}

func (a *App) confirmMagicLink(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		writeError(w, http.StatusUnauthorized, "invalid or expired sign-in link")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := magicLinkPage.Execute(w, token); err != nil {
		return
	}
}

func (a *App) verifyMagicLink(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid sign-in request")
		return
	}
	token := r.PostForm.Get("token")
	if token == "" {
		writeError(w, http.StatusUnauthorized, "invalid or expired sign-in link")
		return
	}
	sessionToken, err := randomToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create session")
		return
	}
	_, err = a.store.ConsumeMagicLink(r.Context(), hashToken(token), hashToken(sessionToken), time.Now().Add(a.config.SessionTTL))
	if errors.Is(err, ErrInvalidLink) {
		writeError(w, http.StatusUnauthorized, "invalid or expired sign-in link")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create session")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: a.config.CookieName, Value: sessionToken, Path: "/", HttpOnly: true,
		Secure: a.cookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: int(a.config.SessionTTL.Seconds()),
	})
	http.Redirect(w, r, "/?signed-in=1", http.StatusSeeOther)
}

func (a *App) withUser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(a.config.CookieName)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "sign in to continue")
			return
		}
		user, err := a.store.UserBySession(r.Context(), hashToken(cookie.Value))
		if errors.Is(err, ErrNotFound) {
			clearCookie(w, a.config.CookieName, a.cookieSecure)
			writeError(w, http.StatusUnauthorized, "sign in to continue")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not check session")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userContextKey{}, user)))
	}
}

func (a *App) currentUser(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, currentUser(r))
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(a.config.CookieName); err == nil {
		_ = a.store.DeleteSession(r.Context(), hashToken(cookie.Value))
	}
	clearCookie(w, a.config.CookieName, a.cookieSecure)
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) listPages(w http.ResponseWriter, r *http.Request) {
	pages, err := a.store.ListPages(r.Context(), currentUser(r).ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load pages")
		return
	}
	writeJSON(w, http.StatusOK, pages)
}

func (a *App) getPage(w http.ResponseWriter, r *http.Request) {
	id, ok := parsePageID(w, r)
	if !ok {
		return
	}
	page, err := a.store.GetPage(r.Context(), currentUser(r).ID, id)
	writePageResult(w, page, err)
}

func (a *App) createPage(w http.ResponseWriter, r *http.Request) {
	title, content, ok := a.pageInput(w, r)
	if !ok {
		return
	}
	page, err := a.store.CreatePage(r.Context(), currentUser(r).ID, title, content)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not save page")
		return
	}
	writeJSON(w, http.StatusCreated, page)
}

func (a *App) updatePage(w http.ResponseWriter, r *http.Request) {
	id, ok := parsePageID(w, r)
	if !ok {
		return
	}
	title, content, ok := a.pageInput(w, r)
	if !ok {
		return
	}
	page, err := a.store.UpdatePage(r.Context(), currentUser(r).ID, id, title, content)
	writePageResult(w, page, err)
}

func (a *App) deletePage(w http.ResponseWriter, r *http.Request) {
	id, ok := parsePageID(w, r)
	if !ok {
		return
	}
	err := a.store.DeletePage(r.Context(), currentUser(r).ID, id)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "page not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not delete page")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) pageInput(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	var input struct {
		Title   string `json:"title"`
		Content string `json:"content"`
	}
	if err := a.decodeJSON(w, r, &input); err != nil {
		writeDecodeError(w, err)
		return "", "", false
	}
	input.Title = strings.TrimSpace(input.Title)
	if input.Title == "" || utf8.RuneCountInString(input.Title) > 100 {
		writeError(w, http.StatusBadRequest, "title must be between 1 and 100 characters")
		return "", "", false
	}
	return input.Title, input.Content, true
}

func (a *App) decodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, a.config.MaxRequestBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			return errRequestTooLarge
		}
		return fmt.Errorf("invalid request: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("invalid request: only one JSON object is allowed")
	}
	return nil
}

func currentUser(r *http.Request) User {
	return r.Context().Value(userContextKey{}).(User)
}

func parsePageID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, "invalid page id")
		return 0, false
	}
	return id, true
}

func writePageResult(w http.ResponseWriter, page Page, err error) {
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "page not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load page")
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeDecodeError(w http.ResponseWriter, err error) {
	if errors.Is(err, errRequestTooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "request body is too large")
		return
	}
	writeError(w, http.StatusBadRequest, err.Error())
}

func randomToken() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func hashToken(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

func clearCookie(w http.ResponseWriter, name string, secure bool) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: -1})
}
