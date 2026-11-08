package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// App is one row of the `Apps` table (back/data/open.ts). `scopes` and `tokens`
// are JSON columns; the API always exposes them as arrays.
type App struct {
	ID           int64
	Name         string
	Scopes       []string
	ClientID     string
	ClientSecret string
	Tokens       []AppToken
	CreatedAt    string
	UpdatedAt    string
}

// AppToken is one issued bearer token. Expiration is a unix timestamp in
// seconds, exactly like the Node implementation.
type AppToken struct {
	Value      string `json:"value"`
	Expiration int64  `json:"expiration"`
}

// AppColumns is the physical column order, so the JSON key order matches the
// plain object Sequelize returns.
const AppColumns = "id, name, scopes, client_id, client_secret, tokens, createdAt, updatedAt"

// AppByID returns one app or nil.
func (s *Store) AppByID(ctx context.Context, id int64) (*App, error) {
	row := s.DB().QueryRowContext(ctx,
		"SELECT "+AppColumns+" FROM Apps WHERE id = ?", id)
	return scanApp(row)
}

// AppByName returns the app with this unique name, or nil.
func (s *Store) AppByName(ctx context.Context, name string) (*App, error) {
	row := s.DB().QueryRowContext(ctx,
		"SELECT "+AppColumns+" FROM Apps WHERE name = ?", name)
	return scanApp(row)
}

// AppByCredentials looks an app up by the client pair used by /api/auth/token.
func (s *Store) AppByCredentials(ctx context.Context, clientID, clientSecret string) (*App, error) {
	row := s.DB().QueryRowContext(ctx,
		"SELECT "+AppColumns+" FROM Apps WHERE client_id = ? AND client_secret = ?",
		clientID, clientSecret)
	return scanApp(row)
}

// ListApps returns every app ordered by id (Sequelize findAll without order).
func (s *Store) ListApps(ctx context.Context) ([]App, error) {
	rows, err := s.DB().QueryContext(ctx, "SELECT "+AppColumns+" FROM Apps ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("list apps: %w", err)
	}
	defer rows.Close()

	var apps []App
	for rows.Next() {
		app, err := scanAppRows(rows)
		if err != nil {
			return nil, err
		}
		apps = append(apps, *app)
	}
	return apps, rows.Err()
}

// InsertApp creates an app and returns the stored row.
func (s *Store) InsertApp(ctx context.Context, app App) (*App, error) {
	scopes, err := json.Marshal(app.Scopes)
	if err != nil {
		return nil, fmt.Errorf("encode scopes: %w", err)
	}
	tokens, err := json.Marshal(app.Tokens)
	if err != nil {
		return nil, fmt.Errorf("encode tokens: %w", err)
	}
	now := SequelizeTime(time.Now())
	result, err := s.DB().ExecContext(ctx,
		"INSERT INTO Apps (name, scopes, client_id, client_secret, tokens, createdAt, updatedAt) VALUES (?, ?, ?, ?, ?, ?, ?)",
		app.Name, string(scopes), app.ClientID, app.ClientSecret, string(tokens), now, now)
	if err != nil {
		return nil, fmt.Errorf("insert app: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.AppByID(ctx, id)
}

// AppUpdate carries the mutable columns. Nil fields stay untouched, which is
// how Sequelize treats an update payload without those keys.
type AppUpdate struct {
	Name         *string
	Scopes       *[]string
	ClientSecret *string
	Tokens       *[]AppToken
}

// UpdateApp writes the supplied columns and refreshes updatedAt.
func (s *Store) UpdateApp(ctx context.Context, id int64, update AppUpdate) error {
	sets := []string{"updatedAt = ?"}
	args := []any{SequelizeTime(time.Now())}
	if update.Name != nil {
		sets = append(sets, "name = ?")
		args = append(args, *update.Name)
	}
	if update.Scopes != nil {
		encoded, err := json.Marshal(*update.Scopes)
		if err != nil {
			return fmt.Errorf("encode scopes: %w", err)
		}
		sets = append(sets, "scopes = ?")
		args = append(args, string(encoded))
	}
	if update.ClientSecret != nil {
		sets = append(sets, "client_secret = ?")
		args = append(args, *update.ClientSecret)
	}
	if update.Tokens != nil {
		encoded, err := json.Marshal(*update.Tokens)
		if err != nil {
			return fmt.Errorf("encode tokens: %w", err)
		}
		sets = append(sets, "tokens = ?")
		args = append(args, string(encoded))
	}
	args = append(args, id)
	if _, err := s.DB().ExecContext(ctx,
		"UPDATE Apps SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...); err != nil {
		return fmt.Errorf("update app: %w", err)
	}
	return nil
}

// DeleteApps removes rows by id and reports how many were deleted.
func (s *Store) DeleteApps(ctx context.Context, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	result, err := s.DB().ExecContext(ctx, "DELETE FROM Apps WHERE id IN ("+placeholders+")", args...)
	if err != nil {
		return 0, fmt.Errorf("delete apps: %w", err)
	}
	return result.RowsAffected()
}

// AnyAppByName reports whether any of the ids belongs to an app with this name.
func (s *Store) AnyAppByName(ctx context.Context, ids []int64, name string) (bool, error) {
	if len(ids) == 0 {
		return false, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+1)
	for _, id := range ids {
		args = append(args, id)
	}
	args = append(args, name)
	var count int64
	err := s.DB().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM Apps WHERE id IN ("+placeholders+") AND name = ?", args...).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("count apps: %w", err)
	}
	return count > 0, nil
}

type appScanner interface {
	Scan(dest ...any) error
}

func scanApp(row *sql.Row) (*App, error) {
	app, err := scanAppRows(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return app, err
}

func scanAppRows(row appScanner) (*App, error) {
	var app App
	var name, scopes, clientID, clientSecret, tokens sql.NullString
	if err := row.Scan(&app.ID, &name, &scopes, &clientID, &clientSecret, &tokens, &app.CreatedAt, &app.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, fmt.Errorf("scan app: %w", err)
	}
	app.Name = name.String
	app.ClientID = clientID.String
	app.ClientSecret = clientSecret.String
	app.Scopes = decodeStringArray(scopes)
	app.Tokens = decodeAppTokens(tokens)
	return &app, nil
}

func decodeStringArray(value sql.NullString) []string {
	out := []string{}
	if !value.Valid || strings.TrimSpace(value.String) == "" || value.String == "null" {
		return out
	}
	if err := json.Unmarshal([]byte(value.String), &out); err != nil {
		return []string{}
	}
	if out == nil {
		return []string{}
	}
	return out
}

func decodeAppTokens(value sql.NullString) []AppToken {
	out := []AppToken{}
	if !value.Valid || strings.TrimSpace(value.String) == "" || value.String == "null" {
		return out
	}
	if err := json.Unmarshal([]byte(value.String), &out); err != nil {
		return []AppToken{}
	}
	if out == nil {
		return []AppToken{}
	}
	return out
}
