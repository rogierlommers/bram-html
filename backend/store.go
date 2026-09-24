package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound    = errors.New("not found")
	ErrInvalidLink = errors.New("invalid or expired magic link")
	ErrRateLimited = errors.New("magic link requested too recently")
)

type User struct {
	ID    int64  `json:"id"`
	Email string `json:"email"`
}

type Page struct {
	ID        int64     `json:"id"`
	Title     string    `json:"title"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
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
`
	_, err := s.db.ExecContext(ctx, schema)
	return err
}

func (s *Store) CreateMagicLink(ctx context.Context, email, tokenHash string, expiresAt time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now()
	if _, err = tx.ExecContext(ctx, `INSERT INTO users (email, created_at) VALUES (?, ?)
ON CONFLICT(email) DO NOTHING`, email, now.Unix()); err != nil {
		return err
	}

	var userID int64
	if err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE email = ?`, email).Scan(&userID); err != nil {
		return err
	}

	var lastCreated sql.NullInt64
	if err = tx.QueryRowContext(ctx, `SELECT MAX(created_at) FROM magic_tokens WHERE user_id = ?`, userID).Scan(&lastCreated); err != nil {
		return err
	}
	if lastCreated.Valid && now.Unix()-lastCreated.Int64 < 60 {
		return ErrRateLimited
	}

	if _, err = tx.ExecContext(ctx, `INSERT INTO magic_tokens (token_hash, user_id, expires_at, created_at)
VALUES (?, ?, ?, ?)`, tokenHash, userID, expiresAt.Unix(), now.Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RevokeMagicLink(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM magic_tokens WHERE token_hash = ?`, tokenHash)
	return err
}

func (s *Store) ConsumeMagicLink(ctx context.Context, tokenHash, sessionHash string, sessionExpiresAt time.Time) (User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()

	now := time.Now().Unix()
	var user User
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
	if err = tx.QueryRowContext(ctx, `SELECT users.id, users.email FROM users
JOIN magic_tokens ON magic_tokens.user_id = users.id WHERE magic_tokens.token_hash = ?`, tokenHash).Scan(&user.ID, &user.Email); err != nil {
		return User{}, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM magic_tokens WHERE user_id = ? AND token_hash <> ?`, user.ID, tokenHash); err != nil {
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
