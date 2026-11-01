package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// AuditEntry は audit_logs の 1 件のイベント（FR-AU01）。Append の入力であり、Query・Get の出力でもある。
// 空文字・nil の任意項目は NULL で保存する。トークンと API キーは DetailJSON に含めないこと。
type AuditEntry struct {
	ID          int64     // 読み出し専用（Query・Get が埋める）。Append は無視する
	TS          time.Time // ゼロなら現在時刻
	Kind        string    // auth / client / token / lease / tool / input / agent / killswitch / settings / update
	Event       string    // 例: lease.acquire, tool.left_click
	ActorType   domain.ActorKind
	ClientID    string // 未登録・host・system は空（NULL）。削除済みのクライアントも NULL（ラベルは残る）
	ClientLabel string // 表示名のスナップショット
	SessionID   string
	LeaseID     string
	TaskID      string // M4: agent_tasks.id
	StepNo      *int
	Tool        string
	Result      string // ok / error / denied
	ErrorCode   string
	DurationMS  *int64
	DetailJSON  json.RawMessage // nil / 空なら NULL。有効な JSON でなければならない
	Thumb       *ThumbMeta      // 読み出し専用（Query・Get が埋める。サムネイルが無ければ nil）。Append は無視する
}

// ThumbMeta は audit_thumbnails の 1 行（監査ログに紐づく縮小スクリーンショットのメタ情報。FR-AU02）。
// 画像本体は ThumbStore の thumbs/ 配下のファイルで、RelPath はそこからの相対パス（絶対パスは保存しない）。
type ThumbMeta struct {
	RelPath   string
	SizeBytes int64
	Width     int
	Height    int
	CreatedAt time.Time // ゼロなら AttachThumb が現在時刻にする
}

// Audit は監査ログ（audit_logs・audit_thumbnails 表）のストア。
// 削除系（PurgeBefore・PurgeThumbsOver）は DB の行を先に消し、消した行のファイルの相対パスを返す。
// ファイル（ThumbStore）の削除は呼び出し側が後で行う（途中で落ちても残るのは孤児ファイルで、起動時の掃除で回収できる。ER「データの保守」）。
type Audit interface {
	Append(ctx context.Context, e AuditEntry) (id int64, err error)
	// Query は条件に合う監査ログを ts 降順（同時刻は id 降順）で 1 ページ返す。next は次のページがあるときの最後の id（無ければ 0）。
	Query(ctx context.Context, q AuditQuery) (entries []AuditEntry, next int64, err error)
	// Get は 1 件（detail_json・サムネイルのメタを含む）。無ければ ErrNotFound。
	Get(ctx context.Context, id int64) (AuditEntry, error)
	// AttachThumb はサムネイルのメタを紐づける（1 ログにつき 0 または 1 枚）。
	// 2 回目は ErrConflict、audit_log_id が無ければ ErrNotFound、RelPath が thumbs/ 配下の相対パスでなければ ErrInvalidArgument。
	AttachThumb(ctx context.Context, auditID int64, t ThumbMeta) error
	// PurgeBefore は ts < t の audit_logs を削除する（audit_thumbnails は CASCADE）。削除した行のサムネイルの RelPath を返す。
	PurgeBefore(ctx context.Context, t time.Time) (removedThumbs []string, err error)
	// PurgeThumbsOver は SUM(size_bytes) が maxBytes を超えるあいだ、created_at の古い順にサムネイルの行だけを削除する
	// （ログ行は残す）。削除した行の RelPath を返す。maxBytes <= 0 は ErrInvalidArgument。
	PurgeThumbsOver(ctx context.Context, maxBytes int64) (removedThumbs []string, err error)
	// ThumbPaths は audit_thumbnails にあるすべての RelPath（孤児ファイルの掃除で ThumbStore.SweepOrphans と組にして使う）。
	ThumbPaths(ctx context.Context) ([]string, error)
	// PurgeMissingThumbs は exists が false を返す RelPath の audit_thumbnails の行を削除し、その件数を返す（ログ行は残す）。
	// exists はトランザクションの外で呼ぶ。
	PurgeMissingThumbs(ctx context.Context, exists func(relPath string) bool) (removed int, err error)
}

type auditStore struct{ db *DB }

func (s auditStore) Append(ctx context.Context, e AuditEntry) (int64, error) {
	ts := e.TS
	if ts.IsZero() {
		ts = s.db.clock.Now()
	}
	var detail sql.NullString
	if len(e.DetailJSON) > 0 {
		if !json.Valid(e.DetailJSON) {
			return 0, fmt.Errorf("%w: audit detail is not valid JSON", domain.ErrInvalidArgument)
		}
		detail = sql.NullString{String: string(e.DetailJSON), Valid: true}
	}
	res, err := s.db.sql.ExecContext(ctx,
		`INSERT INTO audit_logs (ts, kind, event, actor_type, client_id, client_label, session_id,
			lease_id, task_id, step_no, tool, result, error_code, duration_ms, detail_json)
		VALUES (?, ?, ?, ?, (SELECT id FROM clients WHERE id = ?), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		toMS(ts), e.Kind, e.Event, string(e.ActorType), nullStr(e.ClientID), e.ClientLabel,
		nullStr(e.SessionID), nullStr(e.LeaseID), nullStr(e.TaskID), e.StepNo,
		nullStr(e.Tool), e.Result, nullStr(e.ErrorCode), e.DurationMS, detail)
	if err != nil {
		return 0, mapErr(err)
	}
	return res.LastInsertId()
}
