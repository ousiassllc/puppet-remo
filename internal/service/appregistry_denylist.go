package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// 起動の方式（launch.mode）と拒否リスト（launch_denylist）の管理。書き込みはホスト GUI だけ（AppRegistry。NFR-S20）。
// 監査は kind = settings・actor_type = host（settings.launch_mode.update・settings.launch_denylist.create / update / delete）。
// いずれも監査の行を同期で書き、書けなければ変更を元に戻して audit_unavailable を返す（audit_commit.go）。

// launchModeAuditEvent は起動の方式の切り替えの監査のイベント名（SetLaunchMode が同期で書く）。
const launchModeAuditEvent = "settings.launch_mode.update"

func (r *appRegistry) LaunchMode(ctx context.Context) (domain.LaunchMode, error) {
	raw, ok, err := r.cfg.Settings.Get(ctx, settingLaunchMode)
	if err != nil {
		return "", fmt.Errorf("read setting %s: %w", settingLaunchMode, err)
	}
	if !ok {
		return domain.LaunchModeAllowlist, nil
	}
	var m domain.LaunchMode
	if json.Unmarshal(raw, &m) != nil || !m.Valid() {
		return domain.LaunchModeAllowlist, nil // 手で書き換えられた値は、より厳しい方式として扱う（fail closed）
	}
	return m, nil
}

// SetLaunchMode は方式を切り替える（mu で、読み取りから監査・ロールバックまでを 1 つの臨界区間にする）。設定を書いたあとに監査の行を同期で書き、
// 書けなければ設定を元の方式へ戻して失敗を返す（fail-closed。#174。共通の処理は audit_commit.go）。
func (r *appRegistry) SetLaunchMode(ctx context.Context, mode domain.LaunchMode) error {
	if !mode.Valid() {
		return AppFieldErrors{domain.AppFieldLaunchMode: msgLaunchModeInvalid}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	before, err := r.LaunchMode(ctx)
	if err != nil {
		return err
	}
	if before == mode {
		return nil
	}
	raw, err := json.Marshal(mode)
	if err != nil {
		return err
	}
	var undo undoLog
	if err := setSetting(ctx, r.cfg.Settings, &undo, settingLaunchMode, raw); err != nil {
		return err
	}
	entry := r.auditEntry(launchModeAuditEvent, map[string]any{"from": string(before), "to": string(mode)})
	return commitAudited(ctx, r.log, r.cfg.Audit, AuditSubjectLaunchMode, undo, entry)
}

func (r *appRegistry) ListDenied(ctx context.Context) ([]domain.DeniedApp, error) {
	return r.cfg.Denied.List(ctx)
}

func (r *appRegistry) CreateDenied(ctx context.Context, value string) (domain.DeniedApp, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	kind, stored, err := r.prepareDenied(ctx, value)
	if err != nil {
		return domain.DeniedApp{}, err
	}
	now := r.cfg.Clock.Now()
	d := domain.DeniedApp{ID: r.cfg.IDs.NewID(), Kind: kind, Value: stored, CreatedAt: now, UpdatedAt: now}
	if err := r.cfg.Denied.Create(ctx, d, domain.MaxDeniedApps); err != nil {
		return domain.DeniedApp{}, mapDeniedWriteErr(err)
	}
	entry := r.auditEntry("settings.launch_denylist.create", map[string]any{"id": d.ID, "kind": string(d.Kind), "value": d.Value})
	undo := undoLog{func(ctx context.Context) error { return r.cfg.Denied.Delete(ctx, d.ID) }}
	if err := commitAudited(ctx, r.log, r.cfg.Audit, AuditSubjectLaunchDenylist, undo, entry); err != nil {
		return domain.DeniedApp{}, err
	}
	return d, nil
}

func (r *appRegistry) UpdateDenied(ctx context.Context, id, value string) (domain.DeniedApp, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	before, err := r.cfg.Denied.Get(ctx, id)
	if err != nil {
		return domain.DeniedApp{}, mapAppErr(err)
	}
	kind, stored, err := r.prepareDenied(ctx, value)
	if err != nil {
		return domain.DeniedApp{}, err
	}
	d := before
	d.Kind, d.Value, d.UpdatedAt = kind, stored, r.cfg.Clock.Now()
	if err := r.cfg.Denied.Update(ctx, d); err != nil {
		return domain.DeniedApp{}, mapDeniedWriteErr(err)
	}
	entry := r.auditEntry("settings.launch_denylist.update", map[string]any{
		"id": d.ID, "kind": string(d.Kind), "value": d.Value, "before_kind": string(before.Kind), "before_value": before.Value,
	})
	undo := undoLog{func(ctx context.Context) error { return r.cfg.Denied.Update(ctx, before) }}
	if err := commitAudited(ctx, r.log, r.cfg.Audit, AuditSubjectLaunchDenylist, undo, entry); err != nil {
		return domain.DeniedApp{}, err
	}
	return d, nil
}

func (r *appRegistry) DeleteDenied(ctx context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, err := r.cfg.Denied.Get(ctx, id)
	if err != nil {
		return mapAppErr(err)
	}
	if err := r.cfg.Denied.Delete(ctx, id); err != nil {
		return mapAppErr(err)
	}
	entry := r.auditEntry("settings.launch_denylist.delete", map[string]any{"id": d.ID, "kind": string(d.Kind), "value": d.Value})
	undo := undoLog{func(ctx context.Context) error { return r.cfg.Denied.Create(ctx, d, domain.MaxDeniedApps) }}
	return commitAudited(ctx, r.log, r.cfg.Audit, AuditSubjectLaunchDenylist, undo, entry)
}

// prepareDenied は入力 value の形を検査し（domain.ClassifyDenied）、保存する Kind と Value を返す。
// パスは実体のパスへ解決して保存する（照合は実体のパスで行うため。AppLauncher.Resolve）。ファイルが無い・固定ディスクにない・
// 解決できないときは、入力の綴りを区切りだけそろえて保存する（まだ置かれていないファイルや、別のドライブのファイルも拒否リストに持てる）。
func (r *appRegistry) prepareDenied(ctx context.Context, value string) (domain.DeniedKind, string, error) {
	value = strings.TrimSpace(value)
	kind, msg := domain.ClassifyDenied(value)
	if msg != "" {
		return "", "", AppFieldErrors{domain.AppFieldDeniedValue: msg}
	}
	if kind == domain.DeniedName {
		return kind, value, nil
	}
	resolved, err := r.cfg.Launcher.Resolve(ctx, value)
	var le *platform.LaunchError
	switch {
	case err == nil:
		return kind, resolved, nil
	case errors.As(err, &le), errors.Is(err, domain.ErrInvalidArgument):
		return kind, domain.NormalizePathForMatch(value), nil
	}
	return "", "", err
}

// mapDeniedWriteErr は拒否リストの保存の失敗を写す: 重複は項目エラー、200 件到達は conflict、無いは ErrNotFound。
func mapDeniedWriteErr(err error) error {
	switch {
	case errors.Is(err, repository.ErrConflict):
		return AppFieldErrors{domain.AppFieldDeniedValue: msgDeniedTaken}
	case errors.Is(err, repository.ErrLimitReached):
		return domain.NewError(domain.CodeConflict, "the launch denylist is full", nil)
	}
	return mapAppErr(err)
}
