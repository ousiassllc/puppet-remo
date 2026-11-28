package service

import (
	"context"
	"errors"
	"net"

	"github.com/ousiassllc/puppet-remo/internal/codex/appserver"
	"github.com/ousiassllc/puppet-remo/internal/codex/engine"
)

// Install は固定した版のエンジンの取得を始める（非同期。進みと結果は Status で読む）。
func (s *CodexAuthService) Install(_ context.Context) error {
	s.mu.Lock()
	switch {
	case s.closed || s.inst == nil || s.eng.state == CodexEngineUnsupported:
		s.mu.Unlock()
		return conflict("Codex のエンジンは、この環境では取得できません（Windows の x64 と Arm64 だけです）")
	case s.eng.state == CodexEngineInstalling:
		s.mu.Unlock()
		return conflict("Codex のエンジンを取得中です")
	case s.removing:
		s.mu.Unlock()
		return conflict("Codex のエンジンを削除中です")
	}
	ctx, cancel := context.WithCancel(s.baseCtx)
	s.instStop = cancel
	s.eng = codexEngineInfo{state: CodexEngineInstalling}
	s.wg.Add(1)
	s.mu.Unlock()
	go s.runInstall(ctx, cancel)
	return nil
}

// CancelInstall は取得を止める（冪等）。
func (s *CodexAuthService) CancelInstall() {
	s.mu.Lock()
	cancel := s.instStop
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *CodexAuthService) runInstall(ctx context.Context, cancel context.CancelFunc) {
	defer s.wg.Done()
	defer cancel()
	// 動いている app-server は実行ファイルを使っているので、置き換える前に止める（取得の間は起動もしない）。
	s.srv.Stop()
	err := s.inst.Install(ctx, func(p engine.Phase, done, total int64) {
		if total <= 0 {
			return
		}
		pct := int(done * 90 / total) // ダウンロードが 0〜90、展開と検証が 90〜100
		if p == engine.PhaseExtract {
			pct = 90 + int(done*10/total)
		}
		s.mu.Lock()
		if s.eng.state == CodexEngineInstalling {
			s.eng.progress = min(pct, 100)
		}
		s.mu.Unlock()
	})

	s.mu.Lock()
	s.instStop = nil
	switch {
	case err == nil:
		s.eng = codexEngineInfo{state: s.engineStateOnDisk()}
		s.known = false
	case errors.Is(err, context.Canceled):
		s.eng = codexEngineInfo{state: s.engineStateOnDisk()}
	default:
		s.eng = codexEngineInfo{state: CodexEngineFailed, err: installFailureText(err)}
	}
	s.mu.Unlock()

	version := map[string]any{"version": s.cat.Tag}
	switch {
	case err == nil:
		s.record(codexInstallAuditEvent, auditOK, version)
		s.toast("info", "Codex のエンジンを取得しました")
		s.maybeRefresh(false)
	case errors.Is(err, context.Canceled):
		s.record(codexInstallAuditEvent, auditDenied, map[string]any{"version": s.cat.Tag, "reason": "cancelled"})
	default:
		s.log.Warn("codex engine install failed", "err_kind", installFailureKind(err))
		s.record(codexInstallAuditEvent, auditError, map[string]any{"version": s.cat.Tag, "reason": installFailureKind(err)})
		s.toast("error", "Codex のエンジンを取得できませんでした")
	}
}

// installFailureKind は取得の失敗の分類（監査・ログ用。URL・パスを含まない）。
func installFailureKind(err error) string {
	var ne net.Error
	switch {
	case errors.Is(err, engine.ErrMismatch):
		return "mismatch"
	case errors.Is(err, engine.ErrUnsafeArchive):
		return "unsafe_archive"
	case errors.Is(err, engine.ErrInsecureURL):
		return "insecure_url"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne):
		return "network"
	}
	return "failed"
}

// installFailureText は取得の失敗を利用者向けに言う（日本語。URL・パスの秘密を含まない）。
func installFailureText(err error) string {
	switch installFailureKind(err) {
	case "mismatch":
		return "ダウンロードしたファイルが、固定した公式のものと一致しませんでした。もう一度お試しください"
	case "unsafe_archive":
		return "ダウンロードしたアーカイブの形式が想定と違うため、使いませんでした。もう一度お試しください"
	case "insecure_url":
		return "安全でない接続先のため、取得を止めました"
	case "network":
		return "ダウンロードに失敗しました。ネットワークを確認して、もう一度お試しください"
	}
	return "エンジンを取得できませんでした。空き容量と書き込み権限を確認して、もう一度お試しください"
}

// ensure は app-server を（必要なら）起動して返す。起動の失敗はエンジンの状態に反映する
// （エンジンが無いだけなら failed にせず、missing のまま）。
func (s *CodexAuthService) ensure(ctx context.Context) (*appserver.Session, error) {
	se, err := s.srv.Ensure(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case err == nil:
		if s.eng.state == CodexEngineFailed {
			s.eng = codexEngineInfo{state: CodexEngineInstalled}
		}
	case errors.Is(err, errCodexEngineMissing):
		if s.eng.state == CodexEngineInstalled || s.eng.state == CodexEngineOutdated {
			s.eng = codexEngineInfo{state: s.engineStateOnDisk()}
		}
	case ctx.Err() == nil && s.eng.state == CodexEngineInstalled:
		s.eng = codexEngineInfo{state: CodexEngineFailed, err: "Codex のエンジンを起動できませんでした。取得し直してください"}
	}
	return se, err
}
