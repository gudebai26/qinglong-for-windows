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

// Auth row types (back/data/system.ts AuthDataType).
const (
	AuthTypeLoginLog     = "loginLog"
	AuthTypeNotification = "notification"
	AuthTypeAuthConfig   = "authConfig"
	AuthTypeNotifyLog    = "notifyLog"
	AuthTypeSystemConfig = "systemConfig"
)

// AuthRow is one row of the `Auths` table. Sequelize models it as `Auth`.
type AuthRow struct {
	ID        int64
	IP        string
	Type      string
	Info      json.RawMessage
	CreatedAt string
	UpdatedAt string
}

// SequelizeTime renders a timestamp the way Sequelize stores DATETIME values:
// "2026-11-07 04:02:53.167 +00:00" in UTC.
func SequelizeTime(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05.000 -07:00")
}

// EnsureAuthRow mirrors initData.ts: create the row for typ when missing and
// fill an empty info document with defaultInfo.
func (s *Store) EnsureAuthRow(ctx context.Context, typ string, defaultInfo any) error {
	row, err := s.AuthRowByType(ctx, typ)
	if err != nil {
		return err
	}
	if row != nil && len(row.Info) > 0 && string(row.Info) != "null" {
		return nil
	}
	info, err := json.Marshal(defaultInfo)
	if err != nil {
		return err
	}
	if row == nil {
		_, err = s.InsertAuthRow(ctx, typ, info)
		return err
	}
	return s.UpsertAuthRow(ctx, typ, info)
}

// EnsureAuthRowExists creates the row for typ with a NULL info document when it
// is missing. initData.ts uses findOrCreate for the notification row and never
// writes an info value, so the column stays NULL until the panel saves it.
func (s *Store) EnsureAuthRowExists(ctx context.Context, typ string) error {
	row, err := s.AuthRowByType(ctx, typ)
	if err != nil {
		return err
	}
	if row != nil {
		return nil
	}
	now := SequelizeTime(time.Now())
	_, err = s.DB().ExecContext(ctx,
		"INSERT INTO Auths (ip, type, info, createdAt, updatedAt) VALUES (NULL, ?, NULL, ?, ?)",
		typ, now, now)
	if err != nil {
		return fmt.Errorf("create %s auth row: %w", typ, err)
	}
	return nil
}

// AuthRowByType returns the first row of a type, or nil.
func (s *Store) AuthRowByType(ctx context.Context, typ string) (*AuthRow, error) {
	row := s.DB().QueryRowContext(ctx,
		"SELECT id, COALESCE(ip, ''), type, info, createdAt, updatedAt FROM Auths WHERE type = ? ORDER BY id LIMIT 1",
		typ)
	var auth AuthRow
	var info sql.NullString
	if err := row.Scan(&auth.ID, &auth.IP, &auth.Type, &info, &auth.CreatedAt, &auth.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s auth row: %w", typ, err)
	}
	if info.Valid {
		auth.Info = json.RawMessage(info.String)
	}
	return &auth, nil
}

// AuthRowsByType returns every row of a type ordered by id.
func (s *Store) AuthRowsByType(ctx context.Context, typ string) ([]AuthRow, error) {
	rows, err := s.DB().QueryContext(ctx,
		"SELECT id, COALESCE(ip, ''), type, info, createdAt, updatedAt FROM Auths WHERE type = ? ORDER BY id", typ)
	if err != nil {
		return nil, fmt.Errorf("list %s auth rows: %w", typ, err)
	}
	defer rows.Close()

	var out []AuthRow
	for rows.Next() {
		var auth AuthRow
		var info sql.NullString
		if err := rows.Scan(&auth.ID, &auth.IP, &auth.Type, &info, &auth.CreatedAt, &auth.UpdatedAt); err != nil {
			return nil, err
		}
		if info.Valid {
			auth.Info = json.RawMessage(info.String)
		}
		out = append(out, auth)
	}
	return out, rows.Err()
}

// InsertAuthRow appends a row and returns its id.
func (s *Store) InsertAuthRow(ctx context.Context, typ string, info []byte) (int64, error) {
	now := SequelizeTime(time.Now())
	result, err := s.DB().ExecContext(ctx,
		"INSERT INTO Auths (ip, type, info, createdAt, updatedAt) VALUES (NULL, ?, ?, ?, ?)",
		typ, string(info), now, now)
	if err != nil {
		return 0, fmt.Errorf("insert %s auth row: %w", typ, err)
	}
	return result.LastInsertId()
}

// UpsertAuthRow updates the info document of an existing type or inserts it,
// matching updateAuthDb() in back/services/user.ts.
func (s *Store) UpsertAuthRow(ctx context.Context, typ string, info []byte) error {
	row, err := s.AuthRowByType(ctx, typ)
	if err != nil {
		return err
	}
	now := SequelizeTime(time.Now())
	if row == nil {
		_, err = s.DB().ExecContext(ctx,
			"INSERT INTO Auths (ip, type, info, createdAt, updatedAt) VALUES (NULL, ?, ?, ?, ?)",
			typ, string(info), now, now)
		if err != nil {
			return fmt.Errorf("insert %s auth row: %w", typ, err)
		}
		return nil
	}
	_, err = s.DB().ExecContext(ctx,
		"UPDATE Auths SET type = ?, info = ?, updatedAt = ? WHERE id = ?",
		typ, string(info), now, row.ID)
	if err != nil {
		return fmt.Errorf("update %s auth row: %w", typ, err)
	}
	return nil
}

// DeleteAuthRows removes rows by id (used by the login log and notify log
// retention windows).
func (s *Store) DeleteAuthRows(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	_, err := s.DB().ExecContext(ctx, "DELETE FROM Auths WHERE id IN ("+placeholders+")", args...)
	if err != nil {
		return fmt.Errorf("delete auth rows: %w", err)
	}
	return nil
}

// PruneAuthRows keeps the newest `keep` rows of a type. It mirrors
// persistNotificationAudit's retention query (order by id DESC, offset keep).
func (s *Store) PruneAuthRows(ctx context.Context, typ string, keep int) error {
	var oldest int64
	err := s.DB().QueryRowContext(ctx,
		"SELECT id FROM Auths WHERE type = ? ORDER BY id DESC LIMIT 1 OFFSET ?", typ, keep).Scan(&oldest)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("locate %s prune boundary: %w", typ, err)
	}
	_, err = s.DB().ExecContext(ctx, "DELETE FROM Auths WHERE type = ? AND id <= ?", typ, oldest)
	if err != nil {
		return fmt.Errorf("prune %s rows: %w", typ, err)
	}
	return nil
}
