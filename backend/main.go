package main

import (
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/sirupsen/logrus"
)

func main() {
	baseLogger := logrus.New()
	baseLogger.SetOutput(os.Stdout)
	logger := baseLogger.WithField("service", "bram-html")

	dbFile := envOr("DB_FILE", "bram-html.db")
	logger.Infof("using database file %s", dbFile)
	store, err := NewStore(dbFile)
	if err != nil {
		logger.Fatal(err)
	}
	defer store.Close()

	mailer := configuredMailer(logger)
	baseURL := envOr("BASE_URL", "http://localhost:8080")
	app, err := NewApp(store, mailer, Config{
		BaseURL:           baseURL,
		LoginPerIPLimit:   envInt("AUTH_RATE_LIMIT_PER_IP", 0),
		LoginGlobalLimit:  envInt("AUTH_RATE_LIMIT_GLOBAL", 0),
		VerifyPerIPLimit:  envInt("AUTH_VERIFY_RATE_LIMIT_PER_IP", 0),
		VerifyGlobalLimit: envInt("AUTH_VERIFY_RATE_LIMIT_GLOBAL", 0),
		LoginCodeSecret:   os.Getenv("AUTH_CODE_SECRET"),
		StaticDir:         envOr("STATIC_DIR", "frontend"),
	})
	if err != nil {
		logger.Fatal(err)
	}
	server := &http.Server{
		Addr:              envOr("ADDR", ":8080"),
		Handler:           app.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	logger.Printf("listening on %s (%s)", server.Addr, baseURL)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Fatal(err)
	}
}

func configuredMailer(logger *logrus.Entry) Mailer {
	host := os.Getenv("SMTP_HOST")
	if host == "" {
		logger.Print("SMTP is not configured; login codes will be printed here")
		return LogMailer{Logger: logger}
	}
	return SMTPMailer{
		Host: host, Port: envOr("SMTP_PORT", "587"), Username: os.Getenv("SMTP_USERNAME"),
		Password: os.Getenv("SMTP_PASSWORD"), From: envOr("SMTP_FROM", "bram-html@example.com"),
	}
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < -1 {
		return fallback
	}
	return parsed
}
