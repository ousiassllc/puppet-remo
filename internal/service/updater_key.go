package service

import (
	"bytes"
	_ "embed" // updater_pubkey.pub を埋め込む
	"errors"
	"strings"

	"aead.dev/minisign"
)

// ErrUpdateKeyNotConfigured は、アプリに埋め込んだ更新の公開鍵が未設定（プレースホルダのまま・空）であることを表す。
// このとき Updater は、更新の確認はするが、署名を検証できないので適用しない（fail closed）。
var ErrUpdateKeyNotConfigured = errors.New("service: update public key is not configured")

// updatePublicKeyFile は更新の署名の検証に使う公開鍵（minisign の .pub ファイルの中身。FR-H09、NFR-S10）。
// 秘密鍵は GitHub Actions の Secret にだけあり、リポジトリにもログにも置かない。
// 本物の公開鍵はオーナーが生成して置き換える（それまではプレースホルダで、ErrUpdateKeyNotConfigured になる）。
//
//go:embed updater_pubkey.pub
var updatePublicKeyFile []byte

// EmbeddedUpdatePublicKey は埋め込んだ公開鍵の文字列（minisign の .pub の形）を返す。Updater の設定 PublicKey に渡す。
func EmbeddedUpdatePublicKey() []byte { return updatePublicKeyFile }

// parseUpdatePublicKey は minisign の公開鍵ファイルの内容を解釈する。空・コメント行（#）だけのプレースホルダは
// ErrUpdateKeyNotConfigured、書式が不正なら解釈のエラー。
func parseUpdatePublicKey(text []byte) (minisign.PublicKey, error) {
	if isPlaceholderKey(text) {
		return minisign.PublicKey{}, ErrUpdateKeyNotConfigured
	}
	var key minisign.PublicKey
	if err := key.UnmarshalText(bytes.TrimSpace(text)); err != nil {
		return minisign.PublicKey{}, err
	}
	return key, nil
}

// isPlaceholderKey は text が空か、空行と # で始まるコメント行だけかを返す。
func isPlaceholderKey(text []byte) bool {
	for _, line := range strings.Split(string(text), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			return false
		}
	}
	return true
}
