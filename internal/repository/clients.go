package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// Clients は許可リスト（clients 表）のストア。
type Clients interface {
	Get(ctx context.Context, id string) (domain.Client, error)
	ByNode(ctx context.Context, stableNodeID string) (domain.Client, error) // 未登録は ErrNotFound
	List(ctx context.Context) ([]domain.Client, error)
	Create(ctx context.Context, c domain.Client) error              // stable_node_id の重複は ErrConflict
	Update(ctx context.Context, id string, patch ClientPatch) error // approval_mode 変更時は approved_at を NULL に戻す
	Delete(ctx context.Context, id string) error                    // client_tokens は CASCADE
	// Replace は oldID のクライアントを消して c を登録する（同一トランザクション）。stable_node_id が同じ
	// ログイン名変更後の再許可に使う。oldID が無ければ ErrNotFound。失敗したら oldID は残る。
	Replace(ctx context.Context, oldID string, c domain.Client) error
	// MarkApproved は first_time のクライアントが承認された時刻 approved_at を記録する（FR-L07）。無ければ ErrNotFound。
	MarkApproved(ctx context.Context, id string, at time.Time) error
	// ResetApproval は承認済みの記録（approved_at）を NULL に戻す（承認の取り消し。次のリース取得から再び承認が要る）。無ければ ErrNotFound。
	// approval_mode の変更で NULL に戻す規則は Update が持つ。
	ResetApproval(ctx context.Context, id string) error
	TouchSeen(ctx context.Context, id string, at time.Time) error
}

// ClientPatch は Update の部分更新。nil の項目は変えない。
type ClientPatch struct {
	LoginName    *string
	DisplayName  *string
	WebEnabled   *bool
	MCPEnabled   *bool
	AgentEnabled *bool
	ApprovalMode *domain.ApprovalMode
}

type clientStore struct{ db *DB }

const clientColumns = `id, stable_node_id, login_name, display_name,
	web_enabled, mcp_enabled, agent_enabled, approval_mode, approved_at`

func scanClient(row interface{ Scan(...any) error }) (domain.Client, error) {
	var (
		c               domain.Client
		web, mcp, agent bool
		mode            string
		approvedAt      sql.NullInt64
	)
	if err := row.Scan(&c.ID, &c.StableNodeID, &c.LoginName, &c.DisplayName,
		&web, &mcp, &agent, &mode, &approvedAt); err != nil {
		return domain.Client{}, err
	}
	c.WebEnabled, c.MCPEnabled, c.AgentEnabled = web, mcp, agent
	c.ApprovalMode = domain.ApprovalMode(mode)
	c.ApprovedAt = fromNullMS(approvedAt)
	return c, nil
}

func (s clientStore) getBy(ctx context.Context, column, value string) (domain.Client, error) {
	c, err := scanClient(s.db.sql.QueryRowContext(ctx,
		`SELECT `+clientColumns+` FROM clients WHERE `+column+` = ?`, value))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Client{}, ErrNotFound
	}
	return c, err
}

func (s clientStore) Get(ctx context.Context, id string) (domain.Client, error) {
	return s.getBy(ctx, "id", id)
}

func (s clientStore) ByNode(ctx context.Context, stableNodeID string) (domain.Client, error) {
	return s.getBy(ctx, "stable_node_id", stableNodeID)
}

func (s clientStore) List(ctx context.Context) ([]domain.Client, error) {
	rows, err := s.db.sql.QueryContext(ctx,
		`SELECT `+clientColumns+` FROM clients ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []domain.Client
	for rows.Next() {
		c, err := scanClient(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// execer は *sql.DB と *sql.Tx に共通の ExecContext。
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func (s clientStore) Create(ctx context.Context, c domain.Client) error {
	return s.create(ctx, s.db.sql, c)
}

func (s clientStore) create(ctx context.Context, x execer, c domain.Client) error {
	mode := c.ApprovalMode
	if mode == "" {
		mode = domain.ApprovalAlways
	}
	if !mode.Valid() {
		return fmt.Errorf("%w: approval_mode %q", domain.ErrInvalidArgument, mode)
	}
	now := toMS(s.db.clock.Now())
	_, err := x.ExecContext(ctx,
		`INSERT INTO clients (id, stable_node_id, login_name, display_name,
			web_enabled, mcp_enabled, agent_enabled, approval_mode, approved_at,
			created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.StableNodeID, c.LoginName, c.DisplayName,
		c.WebEnabled, c.MCPEnabled, c.AgentEnabled, string(mode), toNullMS(c.ApprovedAt),
		now, now)
	return mapErr(err)
}

func (s clientStore) Update(ctx context.Context, id string, p ClientPatch) error {
	sets := []string{"updated_at = ?"}
	args := []any{toMS(s.db.clock.Now())}
	add := func(set string, v any) {
		sets = append(sets, set)
		args = append(args, v)
	}
	if p.LoginName != nil {
		add("login_name = ?", *p.LoginName)
	}
	if p.DisplayName != nil {
		add("display_name = ?", *p.DisplayName)
	}
	if p.WebEnabled != nil {
		add("web_enabled = ?", *p.WebEnabled)
	}
	if p.MCPEnabled != nil {
		add("mcp_enabled = ?", *p.MCPEnabled)
	}
	if p.AgentEnabled != nil {
		add("agent_enabled = ?", *p.AgentEnabled)
	}
	if p.ApprovalMode != nil {
		if !p.ApprovalMode.Valid() {
			return fmt.Errorf("%w: approval_mode %q", domain.ErrInvalidArgument, *p.ApprovalMode)
		}
		// 承認モードを変えたら承認済みの記録を取り消す（er.md）。SET は更新前の値で評価される。
		sets = append(sets, "approved_at = CASE WHEN approval_mode <> ? THEN NULL ELSE approved_at END")
		args = append(args, string(*p.ApprovalMode))
		add("approval_mode = ?", string(*p.ApprovalMode))
	}
	args = append(args, id)
	return requireOne(s.db.sql.ExecContext(ctx,
		`UPDATE clients SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...))
}

func (s clientStore) Delete(ctx context.Context, id string) error {
	return requireOne(s.db.sql.ExecContext(ctx, `DELETE FROM clients WHERE id = ?`, id))
}

func (s clientStore) Replace(ctx context.Context, oldID string, c domain.Client) (err error) {
	tx, err := s.db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	if err = requireOne(tx.ExecContext(ctx, `DELETE FROM clients WHERE id = ?`, oldID)); err != nil {
		return err
	}
	if err = s.create(ctx, tx, c); err != nil {
		return err
	}
	return tx.Commit()
}

func (s clientStore) TouchSeen(ctx context.Context, id string, at time.Time) error {
	return requireOne(s.db.sql.ExecContext(ctx,
		`UPDATE clients SET last_seen_at = ? WHERE id = ?`, toMS(at), id))
}

func (s clientStore) MarkApproved(ctx context.Context, id string, at time.Time) error {
	return requireOne(s.db.sql.ExecContext(ctx,
		`UPDATE clients SET approved_at = ?, updated_at = ? WHERE id = ?`, toMS(at), toMS(s.db.clock.Now()), id))
}

func (s clientStore) ResetApproval(ctx context.Context, id string) error {
	return requireOne(s.db.sql.ExecContext(ctx,
		`UPDATE clients SET approved_at = NULL, updated_at = ? WHERE id = ?`, toMS(s.db.clock.Now()), id))
}
