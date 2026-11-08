package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Subscription is one row of the `Subscriptions` table. Every nullable column
// keeps its database representation so the API can tell NULL from 0/"".
type Subscription struct {
	ID       int64
	Name     sql.NullString
	URL      sql.NullString
	Schedule sql.NullString
	// IntervalSchedule is the JSON column {type, value}; NULL when unused.
	IntervalSchedule sql.NullString

	Type      sql.NullString
	Whitelist sql.NullString
	Blacklist sql.NullString
	Status    sql.NullInt64

	Dependences sql.NullString
	Extensions  sql.NullString
	SubBefore   sql.NullString
	SubAfter    sql.NullString

	Branch   sql.NullString
	PullType sql.NullString
	// PullOption is the JSON column ({private_key} or {username,password}).
	PullOption sql.NullString

	Pid        sql.NullInt64
	IsDisabled sql.NullInt64
	LogPath    sql.NullString

	ScheduleType sql.NullString
	Alias        sql.NullString
	Proxy        sql.NullString

	AutoAddCron sql.NullInt64
	AutoDelCron sql.NullInt64

	CreatedAt string
	UpdatedAt string
}

// SubscriptionColumns is the physical column order, which keeps the JSON key
// order of the plain object Sequelize returns.
const SubscriptionColumns = "id, name, url, schedule, interval_schedule, type, whitelist, blacklist, status, dependences, extensions, sub_before, sub_after, branch, pull_type, pull_option, pid, is_disabled, log_path, schedule_type, alias, proxy, autoAddCron, autoDelCron, createdAt, updatedAt"

// SubscriptionDefaultOrder mirrors SubscriptionService.list:
// is_disabled ASC, createdAt DESC.
var SubscriptionDefaultOrder = []OrderTerm{
	{Field: "is_disabled"},
	{Field: "createdAt", Desc: true},
}

func scanSubscription(scanner rowScanner) (*Subscription, error) {
	var item Subscription
	if err := scanner.Scan(
		&item.ID, &item.Name, &item.URL, &item.Schedule, &item.IntervalSchedule,
		&item.Type, &item.Whitelist, &item.Blacklist, &item.Status,
		&item.Dependences, &item.Extensions, &item.SubBefore, &item.SubAfter,
		&item.Branch, &item.PullType, &item.PullOption,
		&item.Pid, &item.IsDisabled, &item.LogPath,
		&item.ScheduleType, &item.Alias, &item.Proxy,
		&item.AutoAddCron, &item.AutoDelCron,
		&item.CreatedAt, &item.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &item, nil
}

// ListSubscriptions returns rows matching where (already parameterized).
func (s *Store) ListSubscriptions(ctx context.Context, where string, args []any, order []OrderTerm) ([]Subscription, error) {
	query := "SELECT " + SubscriptionColumns + " FROM Subscriptions"
	if strings.TrimSpace(where) != "" {
		query += " WHERE " + where
	}
	query += orderClause(order)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list subscriptions: %w", err)
	}
	defer rows.Close()

	var items []Subscription
	for rows.Next() {
		item, err := scanSubscription(rows)
		if err != nil {
			return nil, fmt.Errorf("scan subscription: %w", err)
		}
		items = append(items, *item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate subscriptions: %w", err)
	}
	return items, nil
}

// AllSubscriptions returns every row in list order.
func (s *Store) AllSubscriptions(ctx context.Context) ([]Subscription, error) {
	return s.ListSubscriptions(ctx, "", nil, SubscriptionDefaultOrder)
}

// SubscriptionByID loads one row, returning nil when it does not exist.
func (s *Store) SubscriptionByID(ctx context.Context, id int64) (*Subscription, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT "+SubscriptionColumns+" FROM Subscriptions WHERE id = ?", id)
	item, err := scanSubscription(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load subscription %d: %w", id, err)
	}
	return item, nil
}

// InsertSubscription writes a new row and fills in ID plus the timestamps.
func (s *Store) InsertSubscription(ctx context.Context, item *Subscription) error {
	now := SequelizeTime(time.Now())
	if item.CreatedAt == "" {
		item.CreatedAt = now
	}
	if item.UpdatedAt == "" {
		item.UpdatedAt = now
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO Subscriptions
		 (name, url, schedule, interval_schedule, type, whitelist, blacklist, status,
		  dependences, extensions, sub_before, sub_after, branch, pull_type, pull_option,
		  pid, is_disabled, log_path, schedule_type, alias, proxy, autoAddCron, autoDelCron,
		  createdAt, updatedAt)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.Name, item.URL, item.Schedule, item.IntervalSchedule, item.Type,
		item.Whitelist, item.Blacklist, item.Status,
		item.Dependences, item.Extensions, item.SubBefore, item.SubAfter,
		item.Branch, item.PullType, item.PullOption,
		item.Pid, item.IsDisabled, item.LogPath,
		item.ScheduleType, item.Alias, item.Proxy,
		item.AutoAddCron, item.AutoDelCron,
		item.CreatedAt, item.UpdatedAt,
	)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("read inserted subscription id: %w", err)
	}
	item.ID = id
	return nil
}

var subscriptionUpdatable = map[string]struct{}{
	"name": {}, "url": {}, "schedule": {}, "interval_schedule": {}, "type": {},
	"whitelist": {}, "blacklist": {}, "status": {}, "dependences": {},
	"extensions": {}, "sub_before": {}, "sub_after": {}, "branch": {},
	"pull_type": {}, "pull_option": {}, "pid": {}, "is_disabled": {},
	"log_path": {}, "schedule_type": {}, "alias": {}, "proxy": {},
	"autoAddCron": {}, "autoDelCron": {},
}

// UpdateSubscription applies a partial update and always touches updatedAt.
func (s *Store) UpdateSubscription(ctx context.Context, id int64, fields map[string]any) error {
	_, err := s.updateSubscriptionsWhere(ctx, "id = ?", []any{id}, fields)
	return err
}

// UpdateSubscriptions applies the same partial update to a list of ids.
func (s *Store) UpdateSubscriptions(ctx context.Context, ids []int64, fields map[string]any) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	return s.updateSubscriptionsWhere(ctx, "id IN ("+strings.Join(placeholders, ",")+")", args, fields)
}

func (s *Store) updateSubscriptionsWhere(ctx context.Context, where string, args []any, fields map[string]any) (int64, error) {
	if len(fields) == 0 {
		return 0, nil
	}
	sets := make([]string, 0, len(fields)+1)
	values := make([]any, 0, len(fields)+1)
	for key, value := range fields {
		if _, ok := subscriptionUpdatable[key]; !ok {
			continue
		}
		sets = append(sets, quoteIdentifier(key)+" = ?")
		values = append(values, value)
	}
	if len(sets) == 0 {
		return 0, nil
	}
	sets = append(sets, quoteIdentifier("updatedAt")+" = ?")
	values = append(values, SequelizeTime(time.Now()))
	values = append(values, args...)

	query := "UPDATE Subscriptions SET " + strings.Join(sets, ", ")
	if where != "" {
		query += " WHERE " + where
	}
	res, err := s.db.ExecContext(ctx, query, values...)
	if err != nil {
		return 0, err
	}
	affected, _ := res.RowsAffected()
	return affected, nil
}

// DeleteSubscriptions removes rows by id.
func (s *Store) DeleteSubscriptions(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	_, err := s.db.ExecContext(ctx,
		"DELETE FROM Subscriptions WHERE id IN ("+strings.Join(placeholders, ",")+")", args...)
	return err
}
