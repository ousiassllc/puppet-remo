package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"

	"aead.dev/minisign"
)

// このファイルは更新のインストーラの取得と署名の検証（FR-H09、NFR-S10）。署名は minisign（Ed25519）で、インストーラのバイト列全体に対する
// 事前ハッシュ形式（HashEdDSA。minisign の既定）。trusted comment が `version=<版> file=<ファイル名>` に一致することも確かめる。

// updateTempPrefix は検証済みのインストーラを置く一時ディレクトリの名前の接頭辞。
const updateTempPrefix = "puppet-remo-update-"

// 署名の検証・版の束縛の失敗の理由（UpdateInfo.RejectReason・監査の error_code）。
const (
	UpdateRejectSignatureMissing = "signature_missing" // .minisig が無い
	UpdateRejectSignatureInvalid = "signature_invalid" // 改ざん・別の鍵・壊れた署名
	UpdateRejectVersionMismatch  = "version_mismatch"  // 署名の版・ファイル名が Release と合わない（付け替え）
	UpdateRejectKeyUnset         = "key_unset"         // 埋め込みの公開鍵が未設定（検証できないので適用しない）
	UpdateRejectDowngrade        = "downgrade"         // 現行より古い版
)

// rejection は、取得はできたが署名の検証・版の束縛に失敗して拒否したことを表す（通信の失敗とは区別する）。
type rejection struct{ reason string }

func (r *rejection) Error() string { return "update rejected: " + r.reason }

// Unwrap は、拒否を ErrUpdateUnavailable（domain.CodeConflict。適用できる更新がない）として扱えるようにする。
func (r *rejection) Unwrap() error { return ErrUpdateUnavailable }

func rejectionReason(err error) (string, bool) {
	var r *rejection
	if errors.As(err, &r) {
		return r.reason, true
	}
	return "", false
}

// UpdateRejectionReason は、err が署名の検証・版の束縛による拒否のとき、その理由（UpdateReject*）を返す。
func UpdateRejectionReason(err error) (string, bool) { return rejectionReason(err) }

// verifiedInstaller は署名の検証に成功してディスクにあるインストーラ。適用の直前に verifyFile でもう一度確かめる。
type verifiedInstaller struct {
	dir  string // このインストーラ専用の一時ディレクトリ（適用までに消さない）
	path string
	sig  []byte
	tag  string
}

// downloadAndVerify は c の .minisig とインストーラを取得し、署名を検証して専用の一時ディレクトリに置く。
// 通信の失敗は *fetchError、署名・版の束縛の失敗は *rejection（一時ファイルは残さない）。
func (u *updater) downloadAndVerify(ctx context.Context, c updateCandidate) (*verifiedInstaller, error) {
	if !c.HasSig {
		return nil, &rejection{reason: UpdateRejectSignatureMissing}
	}
	if c.AssetSize > u.maxInstaller {
		return nil, &fetchError{code: UpdateErrTooLarge}
	}
	sigBytes, err := u.downloadSignature(ctx, c)
	if err != nil {
		return nil, err
	}
	// 署名の中身の検査（検証の前の早期の拒否。本検証は下の verifyFile で、そちらが最終の判定）。
	var sig minisign.Signature
	if err := sig.UnmarshalText(sigBytes); err != nil || sig.Algorithm != minisign.HashEdDSA || sig.KeyID != u.pub.ID() {
		return nil, &rejection{reason: UpdateRejectSignatureInvalid}
	}
	if sig.TrustedComment != UpdateTrustedComment(c.Version.String(), c.Asset) {
		return nil, &rejection{reason: UpdateRejectVersionMismatch}
	}

	dir, err := os.MkdirTemp(u.cfg.TempDir, updateTempPrefix)
	if err != nil {
		return nil, fmt.Errorf("create update directory: %w", err)
	}
	vi := &verifiedInstaller{dir: dir, path: filepath.Join(dir, c.Asset), sig: sigBytes, tag: c.Tag}
	if err := u.downloadInstaller(ctx, c, vi); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	if err := u.verifyFile(vi.path, vi.sig, c); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return vi, nil
}

// downloadURL は Release の添付ファイルの URL。応答の値は使わず、タグとファイル名から組み立てる。
func (u *updater) downloadURL(c updateCandidate, name string) string {
	return u.cfg.DownloadBase + "/" + u.cfg.Repo + "/releases/download/" + url.PathEscape(c.Tag) + "/" + url.PathEscape(name)
}

// downloadSignature は <インストーラ>.minisig を取得する（上限 4 KiB）。404 は署名の欠落として拒否する。
func (u *updater) downloadSignature(ctx context.Context, c updateCandidate) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, apiRequestTimeout)
	defer cancel()
	resp, err := u.get(ctx, u.downloadURL(c, c.Asset+".minisig"), "application/octet-stream")
	if err != nil {
		var fe *fetchError
		if errors.As(err, &fe) && fe.code == UpdateErrNotFound {
			return nil, &rejection{reason: UpdateRejectSignatureMissing}
		}
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSignatureBytes+1))
	if err != nil {
		return nil, &fetchError{code: UpdateErrNetwork, err: scrubError(err)}
	}
	if len(body) > maxSignatureBytes {
		return nil, &rejection{reason: UpdateRejectSignatureInvalid}
	}
	return body, nil
}

// downloadInstaller はインストーラを vi.path へ書く（上限 300 MiB。新規作成のみで、既存のファイルは上書きしない）。
func (u *updater) downloadInstaller(ctx context.Context, c updateCandidate, vi *verifiedInstaller) error {
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	resp, err := u.get(ctx, u.downloadURL(c, c.Asset), "application/octet-stream")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.ContentLength > u.maxInstaller {
		return &fetchError{code: UpdateErrTooLarge}
	}
	f, err := os.OpenFile(vi.path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create installer file: %w", err)
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, u.maxInstaller+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return &fetchError{code: UpdateErrNetwork, err: scrubError(err)}
	}
	if n > u.maxInstaller {
		return &fetchError{code: UpdateErrTooLarge}
	}
	return nil
}

// verifyFile は path のファイルが、公開鍵 u.pub による sigText の署名と一致し、署名の trusted comment が c の版・ファイル名と
// 一致することを確かめる。ファイルは全体を事前ハッシュにかけて読む（メモリに載せない）。失敗は *rejection。
func (u *updater) verifyFile(path string, sigText []byte, c updateCandidate) error {
	var sig minisign.Signature
	if err := sig.UnmarshalText(sigText); err != nil {
		return &rejection{reason: UpdateRejectSignatureInvalid}
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open installer: %w", scrubFileError(err))
	}
	defer func() { _ = f.Close() }()
	r := minisign.NewReader(f)
	if _, err := io.Copy(io.Discard, r); err != nil {
		return fmt.Errorf("read installer: %w", scrubFileError(err))
	}
	if !r.Verify(u.pub, sigText) {
		return &rejection{reason: UpdateRejectSignatureInvalid}
	}
	// Verify は trusted comment の署名も確かめるので、ここから先の sig.TrustedComment は署名者が書いたもの。
	if sig.TrustedComment != UpdateTrustedComment(c.Version.String(), c.Asset) {
		return &rejection{reason: UpdateRejectVersionMismatch}
	}
	return nil
}

// scrubFileError は *fs.PathError からパスを取り除く（一時ディレクトリの場所をログ・監査へ出さない）。
func scrubFileError(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return fmt.Errorf("%s: %w", pe.Op, pe.Err)
	}
	return err
}
