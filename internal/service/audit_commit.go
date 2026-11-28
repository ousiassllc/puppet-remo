package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// このファイルは、設定の変更を監査の行とそろえて確定する共通の処理（fail-closed。#174・#182）。
//
// ホスト所有者の設定変更（起動の方式・許可リスト・拒否リスト・設定画面・エージェント設定・API キー）は、
// 「変更を書く → 監査の行を同期で書く → 書けなければ変更を元に戻して audit_unavailable（503）を返す」の順に行う。
// 成功を返すのは、変更と監査の行がそろったときだけ。設定と監査は別のストアなので 1 つのトランザクションにはできず、
// 監査を先に書く順にすると、変更の書き込みが失敗したときに「変更した」行だけが残る。そのため変更が先で、失敗したら戻す。
// 呼び出し側は変更の書き込みから commitAudited までを、変更の対象ごとの mutex で 1 つの臨界区間にする。排他しないと、
// 監査に失敗した呼び出しのロールバックが、並行して成功した別の変更を取り消し、設定と監査の行が食い違う。
//
// fail-closed の対象にしないもの（理由）:
//   - キルスイッチの発動・解除（killswitch.state）: 発動を監査の失敗で止めてはならない（緊急停止が最優先）。自前の監査（killswitch.*）は fail-open。
//   - エージェント・高速パスのシステム側の無効化（DisableAgent・DisableFastpath。401 を受けた保護の無効化）: 同じ理由で、監査の失敗で
//     無効化を取り消さない。
//   - update.last_checked_at: 更新の確認の時刻をシステムが書く内部の値で、所有者の設定変更ではなく、監査しない。
//   - クライアントの登録・変更・削除（client.*）: kind = client のクライアント管理で、設定画面の項目ではない（削除はトークン・リースの
//     失効を伴い、元に戻せない）。従来どおり非同期（best-effort）で監査する。

// 監査に書けなかった設定変更の audit_unavailable が持つ Detail のキー。
const (
	// DetailAuditSubject は、変更の対象（AuditSubject*）。GUI が文言を選ぶのに使う。
	DetailAuditSubject = "subject"
	// DetailAuditRestored は、変更を元に戻せたか。戻す書き込みも失敗したときだけ false が付く（変更が残っている可能性がある）。
	DetailAuditRestored = "restored"
)

// 設定変更の対象（DetailAuditSubject の値）。
const (
	AuditSubjectLaunchMode     = "launch_mode"     // 起動の方式（launch.mode）
	AuditSubjectLaunchableApp  = "launchable_app"  // 起動許可リストの登録・変更・削除
	AuditSubjectLaunchDenylist = "launch_denylist" // 拒否リストの登録・変更・削除
	AuditSubjectHostSettings   = "host_settings"   // 設定画面の項目（settings.update）
	AuditSubjectAgentSettings  = "agent_settings"  // エージェントの設定
	AuditSubjectFastpath       = "fastpath"        // 高速パスの設定
	AuditSubjectAPIKey         = "api_key"         // OpenAI・TypeSafe の API キーの登録・削除
)

// AuditChangeFailure は err が、設定変更を監査に書けなかった audit_unavailable かを返す。subject は変更の対象（AuditSubject*）、
// restored は元に戻せたか（false なら変更が残っている可能性がある）。起動の試行の audit_unavailable（対象を持たない）は ok = false。
func AuditChangeFailure(err error) (subject string, restored, ok bool) {
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodeAuditUnavailable {
		return "", false, false
	}
	subject, ok = de.Detail[DetailAuditSubject].(string)
	if !ok {
		return "", false, false
	}
	restored = true
	if v, has := de.Detail[DetailAuditRestored].(bool); has {
		restored = v
	}
	return subject, restored, true
}

// undoLog は、書き込んだ変更を戻す手順を、書いた順に溜める。rollback は逆の順に実行する。
type undoLog []func(ctx context.Context) error

func (u *undoLog) add(f func(ctx context.Context) error) { *u = append(*u, f) }

// rollback は溜めた手順を逆の順にすべて実行し、失敗をまとめて返す（1 つ失敗しても残りは試す）。
func (u undoLog) rollback(ctx context.Context) error {
	var errs []error
	for i := len(u) - 1; i >= 0; i-- {
		if err := u[i](ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// setSetting は設定 key に raw を書き、書き込む前の値へ戻す手順を undo に積む（行が無かったなら、戻すときは行を消す）。
func setSetting(ctx context.Context, store repository.Settings, undo *undoLog, key string, raw json.RawMessage) error {
	prev, had, err := store.Get(ctx, key)
	if err != nil {
		return err
	}
	if err := store.Set(ctx, key, raw); err != nil {
		return err
	}
	undo.add(func(ctx context.Context) error {
		if had {
			return store.Set(ctx, key, prev)
		}
		return store.Delete(ctx, key)
	})
	return nil
}

// commitAudited は、書き終えた設定変更を、監査の行（entries。順に同期で書く）で確定する。audit が nil（テスト用）なら何もしない。
// 書けなければ undo で変更を戻して、対象（subject）つきの domain.ErrAuditUnavailable を返す。戻す書き込みは ctx の取り消しから切り離す
// （取り消された ctx でも戻せるように）。戻す手順も失敗したときは Detail の restored = false で伝える（変更が残っている可能性がある。
// 呼び出し元は、戻せなかったことが分かる文言で返す）。
// entries が 2 行以上で、2 行目以降の書き込みが失敗したときは、先に書いた行が残る（監査は追記だけで消せない）。
func commitAudited(ctx context.Context, log *slog.Logger, audit SyncAuditRecorder, subject string, undo undoLog, entries ...repository.AuditEntry) error {
	if audit == nil {
		return nil
	}
	var auditErr error
	for _, e := range entries {
		if auditErr = audit.RecordSync(ctx, e); auditErr != nil {
			break
		}
	}
	if auditErr == nil {
		return nil
	}
	rollbackErr := undo.rollback(context.WithoutCancel(ctx))
	log.Error("settings change refused: the audit entry could not be written", "subject", subject,
		"audit_err", auditErr, "rolled_back", rollbackErr == nil, "rollback_err", rollbackErr)
	if rollbackErr != nil {
		return domain.ErrAuditUnavailable.
			WithMsg("the audit log is unavailable, and the change could not be restored; check the current state").
			WithDetail(DetailAuditSubject, subject).WithDetail(DetailAuditRestored, false)
	}
	return domain.ErrAuditUnavailable.
		WithMsg("the audit log is unavailable, so the change was not applied").
		WithDetail(DetailAuditSubject, subject)
}
