package main

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

const maxLoginCodeAttempts = 5

var (
	ErrNotFound    = errors.New("not found")
	ErrInvalidCode = errors.New("invalid or expired login code")
	ErrInvalidLink = errors.New("invalid or expired magic link")
	ErrRateLimited = errors.New("login code requested too recently")
)

type User struct {
	ID      int64  `json:"id"`
	Email   string `json:"email"`
	IsAdmin bool   `json:"isAdmin"`
}

type Page struct {
	ID        int64     `json:"id"`
	Title     string    `json:"title"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type AdminSummary struct {
	TotalUsers  int `json:"totalUsers"`
	TotalPages  int `json:"totalPages"`
	OnlineUsers int `json:"onlineUsers"`
}

type DailyActivity struct {
	Date         string `json:"date"`
	NewUsers     int    `json:"newUsers"`
	PagesCreated int    `json:"pagesCreated"`
	ActiveUsers  int    `json:"activeUsers"`
}

type AdminPage struct {
	ID         int64     `json:"id"`
	Title      string    `json:"title"`
	OwnerEmail string    `json:"ownerEmail"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

type AdminStats struct {
	Summary  AdminSummary    `json:"summary"`
	Activity []DailyActivity `json:"activity"`
	Pages    []AdminPage     `json:"pages"`
}

type Store struct {
	db *sql.DB
}

func NewStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)

	store := &Store{db: db}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	const schema = `
PRAGMA foreign_keys = ON;
PRAGMA journal_mode = WAL;

CREATE TABLE IF NOT EXISTS users (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  email TEXT NOT NULL UNIQUE,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_users_created ON users(created_at);

CREATE TABLE IF NOT EXISTS login_codes (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  code_hash TEXT NOT NULL,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at INTEGER NOT NULL,
  used_at INTEGER,
  active INTEGER NOT NULL DEFAULT 0,
  attempts INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_login_codes_user_created ON login_codes(user_id, created_at);
CREATE INDEX IF NOT EXISTS idx_login_codes_expires ON login_codes(expires_at);

CREATE TABLE IF NOT EXISTS magic_tokens (
  token_hash TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at INTEGER NOT NULL,
  used_at INTEGER,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_magic_tokens_user_created ON magic_tokens(user_id, created_at);
CREATE INDEX IF NOT EXISTS idx_magic_tokens_expires ON magic_tokens(expires_at);

CREATE TABLE IF NOT EXISTS sessions (
  token_hash TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at INTEGER NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);

CREATE TABLE IF NOT EXISTS pages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  title TEXT NOT NULL,
  content TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_pages_user_updated ON pages(user_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_pages_updated ON pages(updated_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_pages_created ON pages(created_at);

CREATE TABLE IF NOT EXISTS user_activity (
  user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  last_seen_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_user_activity_last_seen ON user_activity(last_seen_at);

CREATE TABLE IF NOT EXISTS user_activity_days (
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  activity_date TEXT NOT NULL,
  PRIMARY KEY (user_id, activity_date)
);
CREATE INDEX IF NOT EXISTS idx_user_activity_days_date ON user_activity_days(activity_date);
`
	_, err := s.db.ExecContext(ctx, schema)
	return err
}

func (s *Store) CreateLoginCode(ctx context.Context, email, codeHash string, expiresAt time.Time) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	now := time.Now()
	if _, err = tx.ExecContext(ctx, `INSERT INTO users (email, created_at) VALUES (?, ?)
ON CONFLICT(email) DO NOTHING`, email, now.Unix()); err != nil {
		return 0, err
	}

	var userID int64
	if err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE email = ?`, email).Scan(&userID); err != nil {
		return 0, err
	}

	var lastCreated sql.NullInt64
	if err = tx.QueryRowContext(ctx, `SELECT MAX(created_at) FROM login_codes WHERE user_id = ?`, userID).Scan(&lastCreated); err != nil {
		return 0, err
	}
	if lastCreated.Valid && now.Unix()-lastCreated.Int64 < 60 {
		return 0, ErrRateLimited
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM login_codes
WHERE user_id = ? AND (used_at IS NOT NULL OR expires_at < ?)`, userID, now.Unix()); err != nil {
		return 0, err
	}

	result, err := tx.ExecContext(ctx, `INSERT INTO login_codes (code_hash, user_id, expires_at, created_at)
VALUES (?, ?, ?, ?)`, codeHash, userID, expiresAt.Unix(), now.Unix())
	if err != nil {
		return 0, err
	}
	codeID, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return codeID, nil
}

func (s *Store) RevokeLoginCode(ctx context.Context, codeID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM login_codes WHERE id = ?`, codeID)
	return err
}

func (s *Store) ActivateLoginCode(ctx context.Context, codeID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var userID int64
	if err = tx.QueryRowContext(ctx, `SELECT user_id FROM login_codes WHERE id = ?`, codeID).Scan(&userID); err != nil {
		return err
	}
	now := time.Now().Unix()
	if _, err = tx.ExecContext(ctx, `UPDATE login_codes
SET active = CASE WHEN id = ? THEN 1 ELSE 0 END,
    used_at = CASE WHEN id = ? THEN NULL ELSE COALESCE(used_at, ?) END
WHERE user_id = ?`, codeID, codeID, now, userID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ConsumeLoginCode(ctx context.Context, email, submittedHash, sessionHash string, sessionExpiresAt time.Time) (User, error) {
	for attempt := 0; attempt < 3; attempt++ {
		now := time.Now().Unix()
		var codeID, userID int64
		var codeHash, storedEmail string
		var failedAttempts int
		err := s.db.QueryRowContext(ctx, `SELECT login_codes.id, login_codes.code_hash, login_codes.attempts, users.id, users.email
FROM login_codes JOIN users ON users.id = login_codes.user_id
WHERE users.email = ? AND login_codes.active = 1 AND login_codes.used_at IS NULL AND login_codes.expires_at >= ?
LIMIT 1`, email, now).Scan(&codeID, &codeHash, &failedAttempts, &userID, &storedEmail)
		if errors.Is(err, sql.ErrNoRows) {
			return User{}, ErrInvalidCode
		}
		if err != nil {
			return User{}, err
		}

		matches := subtle.ConstantTimeCompare([]byte(codeHash), []byte(submittedHash)) == 1
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return User{}, err
		}
		if !matches {
			nextAttempts := failedAttempts + 1
			result, updateErr := tx.ExecContext(ctx, `UPDATE login_codes
SET attempts = ?, active = CASE WHEN ? >= ? THEN 0 ELSE active END,
    used_at = CASE WHEN ? >= ? THEN ? ELSE used_at END
WHERE id = ? AND attempts = ? AND active = 1 AND used_at IS NULL AND expires_at >= ?`,
				nextAttempts, nextAttempts, maxLoginCodeAttempts, nextAttempts, maxLoginCodeAttempts, now,
				codeID, failedAttempts, now)
			if updateErr != nil {
				_ = tx.Rollback()
				return User{}, updateErr
			}
			changed, rowsErr := result.RowsAffected()
			if rowsErr != nil {
				_ = tx.Rollback()
				return User{}, rowsErr
			}
			if changed == 1 {
				if err = tx.Commit(); err != nil {
					return User{}, err
				}
				return User{}, ErrInvalidCode
			}
			_ = tx.Rollback()
			continue
		}

		result, err := tx.ExecContext(ctx, `UPDATE login_codes SET active = 0, used_at = ?
WHERE id = ? AND attempts = ? AND active = 1 AND used_at IS NULL AND expires_at >= ?`, now, codeID, failedAttempts, now)
		if err != nil {
			_ = tx.Rollback()
			return User{}, err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			_ = tx.Rollback()
			return User{}, err
		}
		if changed != 1 {
			_ = tx.Rollback()
			continue
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO sessions (token_hash, user_id, expires_at, created_at)
VALUES (?, ?, ?, ?)`, sessionHash, userID, sessionExpiresAt.Unix(), now); err != nil {
			_ = tx.Rollback()
			return User{}, err
		}
		if err = tx.Commit(); err != nil {
			return User{}, err
		}
		return User{ID: userID, Email: storedEmail}, nil
	}
	return User{}, ErrInvalidCode
}

func (s *Store) ConsumeMagicLink(ctx context.Context, tokenHash, sessionHash string, sessionExpiresAt time.Time) (User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()

	now := time.Now().Unix()
	result, err := tx.ExecContext(ctx, `UPDATE magic_tokens SET used_at = ?
WHERE token_hash = ? AND used_at IS NULL AND expires_at >= ?`, now, tokenHash, now)
	if err != nil {
		return User{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return User{}, err
	}
	if changed != 1 {
		return User{}, ErrInvalidLink
	}

	var user User
	if err = tx.QueryRowContext(ctx, `SELECT users.id, users.email FROM users
JOIN magic_tokens ON magic_tokens.user_id = users.id WHERE magic_tokens.token_hash = ?`, tokenHash).Scan(&user.ID, &user.Email); err != nil {
		return User{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO sessions (token_hash, user_id, expires_at, created_at)
VALUES (?, ?, ?, ?)`, sessionHash, user.ID, sessionExpiresAt.Unix(), now); err != nil {
		return User{}, err
	}
	if err = tx.Commit(); err != nil {
		return User{}, err
	}
	return user, nil
}

func (s *Store) UserBySession(ctx context.Context, sessionHash string) (User, error) {
	var user User
	err := s.db.QueryRowContext(ctx, `SELECT users.id, users.email FROM users
JOIN sessions ON sessions.user_id = users.id
WHERE sessions.token_hash = ? AND sessions.expires_at >= ?`, sessionHash, time.Now().Unix()).Scan(&user.ID, &user.Email)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return user, err
}

func (s *Store) DeleteSession(ctx context.Context, sessionHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, sessionHash)
	return err
}

func (s *Store) TouchActivity(ctx context.Context, userID int64, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO user_activity (user_id, last_seen_at) VALUES (?, ?)
ON CONFLICT(user_id) DO UPDATE SET last_seen_at = excluded.last_seen_at`, userID, now.Unix()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO user_activity_days (user_id, activity_date) VALUES (?, ?)
ON CONFLICT(user_id, activity_date) DO NOTHING`, userID, now.UTC().Format(time.DateOnly)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AdminStats(ctx context.Context, now time.Time, days int, onlineWindow time.Duration) (AdminStats, error) {
	stats := AdminStats{Activity: make([]DailyActivity, days), Pages: make([]AdminPage, 0)}
	start := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -(days - 1))
	byDate := make(map[string]*DailyActivity, days)
	for index := range stats.Activity {
		day := DailyActivity{Date: start.AddDate(0, 0, index).Format(time.DateOnly)}
		stats.Activity[index] = day
		byDate[day.Date] = &stats.Activity[index]
	}

	if err := s.db.QueryRowContext(ctx, `SELECT
  (SELECT COUNT(*) FROM users),
  (SELECT COUNT(*) FROM pages),
  (SELECT COUNT(*) FROM user_activity WHERE last_seen_at >= ?)`,
		now.Add(-onlineWindow).Unix()).Scan(&stats.Summary.TotalUsers, &stats.Summary.TotalPages, &stats.Summary.OnlineUsers); err != nil {
		return AdminStats{}, err
	}

	queries := []struct {
		query string
		apply func(*DailyActivity, int)
	}{
		{`SELECT date(created_at, 'unixepoch'), COUNT(*) FROM users WHERE created_at >= ? GROUP BY 1`, func(day *DailyActivity, count int) { day.NewUsers = count }},
		{`SELECT date(created_at, 'unixepoch'), COUNT(*) FROM pages WHERE created_at >= ? GROUP BY 1`, func(day *DailyActivity, count int) { day.PagesCreated = count }},
		{`SELECT activity_date, COUNT(*) FROM user_activity_days WHERE activity_date >= ? GROUP BY activity_date`, func(day *DailyActivity, count int) { day.ActiveUsers = count }},
	}
	for index, item := range queries {
		var argument any = start.Unix()
		if index == 2 {
			argument = start.Format(time.DateOnly)
		}
		rows, err := s.db.QueryContext(ctx, item.query, argument)
		if err != nil {
			return AdminStats{}, err
		}
		for rows.Next() {
			var date string
			var count int
			if err := rows.Scan(&date, &count); err != nil {
				_ = rows.Close()
				return AdminStats{}, err
			}
			if day := byDate[date]; day != nil {
				item.apply(day, count)
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return AdminStats{}, err
		}
		if err := rows.Close(); err != nil {
			return AdminStats{}, err
		}
	}

	rows, err := s.db.QueryContext(ctx, `SELECT pages.id, pages.title, users.email, pages.created_at, pages.updated_at
FROM pages JOIN users ON users.id = pages.user_id
ORDER BY pages.updated_at DESC, pages.id DESC`)
	if err != nil {
		return AdminStats{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var page AdminPage
		var createdAt, updatedAt int64
		if err := rows.Scan(&page.ID, &page.Title, &page.OwnerEmail, &createdAt, &updatedAt); err != nil {
			return AdminStats{}, err
		}
		page.CreatedAt = time.Unix(createdAt, 0).UTC()
		page.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		stats.Pages = append(stats.Pages, page)
	}
	if err := rows.Err(); err != nil {
		return AdminStats{}, err
	}
	return stats, nil
}

func (s *Store) ListPages(ctx context.Context, userID int64) ([]Page, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, title, created_at, updated_at FROM pages
WHERE user_id = ? ORDER BY updated_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	pages := make([]Page, 0)
	for rows.Next() {
		var page Page
		var createdAt, updatedAt int64
		if err := rows.Scan(&page.ID, &page.Title, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		page.CreatedAt = time.Unix(createdAt, 0).UTC()
		page.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		pages = append(pages, page)
	}
	return pages, rows.Err()
}

func (s *Store) GetPage(ctx context.Context, userID, id int64) (Page, error) {
	var page Page
	var createdAt, updatedAt int64
	err := s.db.QueryRowContext(ctx, `SELECT id, title, content, created_at, updated_at FROM pages
WHERE id = ? AND user_id = ?`, id, userID).Scan(&page.ID, &page.Title, &page.Content, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Page{}, ErrNotFound
	}
	page.CreatedAt = time.Unix(createdAt, 0).UTC()
	page.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return page, err
}

func (s *Store) CreatePage(ctx context.Context, userID int64, title, content string) (Page, error) {
	now := time.Now().Unix()
	var page Page
	var createdAt, updatedAt int64
	err := s.db.QueryRowContext(ctx, `INSERT INTO pages (user_id, title, content, created_at, updated_at)
VALUES (?, ?, ?, ?, ?) RETURNING id, title, content, created_at, updated_at`, userID, title, content, now, now).
		Scan(&page.ID, &page.Title, &page.Content, &createdAt, &updatedAt)
	page.CreatedAt = time.Unix(createdAt, 0).UTC()
	page.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return page, err
}

func (s *Store) UpdatePage(ctx context.Context, userID, id int64, title, content string) (Page, error) {
	var page Page
	var createdAt, updatedAt int64
	err := s.db.QueryRowContext(ctx, `UPDATE pages SET title = ?, content = ?, updated_at = ?
WHERE id = ? AND user_id = ? RETURNING id, title, content, created_at, updated_at`, title, content, time.Now().Unix(), id, userID).
		Scan(&page.ID, &page.Title, &page.Content, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Page{}, ErrNotFound
	}
	page.CreatedAt = time.Unix(createdAt, 0).UTC()
	page.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return page, err
}

func (s *Store) DeletePage(ctx context.Context, userID, id int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM pages WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return fmt.Errorf("delete page: %w", ErrNotFound)
	}
	return nil
}
