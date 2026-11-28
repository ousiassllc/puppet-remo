package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/ousiassllc/puppet-remo/internal/platform"
)

// このファイルは ChatGPT の OAuth トークンの記録の保管（NFR-S14）。記録（アクセス・リフレッシュ・ID トークン・期限・scope）は
// 資格情報マネージャーの 1 件の上限（2560 バイト）を越えるので、分割して置く:
//
//	puppet-remo/chatgpt     見出し {"v":1,"parts":N}
//	puppet-remo/chatgpt/1…N 記録の JSON を 2000 バイト以下ずつに切ったもの
//
// 書き込みは新しい parts → 見出しの順で、余った古い parts は消す。読むとき、見出しの不整合・parts の欠け・JSON の不備は
// 「トークンなし」として扱う（リフレッシュトークンが回転するので、部分的な記録を使わない）。値はエラー・ログに出さない。

const (
	chatgptTokenPartMax   = 2000 // 1 つの part の最大バイト数
	chatgptTokenMaxParts  = 32   // 見出しが認める parts の最大数（異常な記録を読まない・書かない）
	chatgptTokenHeaderVer = 1
)

// chatgptTokenRecord は保管する 1 件の記録。更新ではまとめて置き換える（回転するリフレッシュトークンの片方だけを置き換えない）。
type chatgptTokenRecord struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	IDToken      string    `json:"id_token,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
	Scope        string    `json:"scope,omitempty"` // 空白区切り
}

type chatgptTokenHeader struct {
	V     int `json:"v"`
	Parts int `json:"parts"`
}

type chatgptTokenStore struct{ secrets platform.SecretStore }

func chatgptPartName(i int) string { return platform.SecretChatGPT + "/" + strconv.Itoa(i) }

// load は記録を読む。無い・不整合・欠けは found = false。資格情報マネージャーの読み出しの失敗は err（記録は「なし」とは限らない）。
func (t chatgptTokenStore) load() (rec chatgptTokenRecord, found bool, err error) {
	raw, err := t.secrets.Get(platform.SecretChatGPT)
	if errors.Is(err, platform.ErrNotFound) {
		return chatgptTokenRecord{}, false, nil
	}
	if err != nil {
		return chatgptTokenRecord{}, false, fmt.Errorf("service: read chatgpt tokens: %w", err)
	}
	var h chatgptTokenHeader
	if json.Unmarshal([]byte(raw), &h) != nil || h.V != chatgptTokenHeaderVer || h.Parts < 1 || h.Parts > chatgptTokenMaxParts {
		return chatgptTokenRecord{}, false, nil
	}
	body := make([]byte, 0, h.Parts*chatgptTokenPartMax)
	for i := 1; i <= h.Parts; i++ {
		part, err := t.secrets.Get(chatgptPartName(i))
		if errors.Is(err, platform.ErrNotFound) {
			return chatgptTokenRecord{}, false, nil
		}
		if err != nil {
			return chatgptTokenRecord{}, false, fmt.Errorf("service: read chatgpt tokens: %w", err)
		}
		body = append(body, part...)
	}
	if json.Unmarshal(body, &rec) != nil || rec.AccessToken == "" || rec.RefreshToken == "" {
		return chatgptTokenRecord{}, false, nil
	}
	return rec, true, nil
}

// save は記録を置き換える（新しい parts → 見出し → 余った古い parts の掃除）。
func (t chatgptTokenStore) save(rec chatgptTokenRecord) error {
	body, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("service: encode chatgpt tokens: %w", err)
	}
	parts := splitUTF8(string(body), chatgptTokenPartMax)
	if len(parts) > chatgptTokenMaxParts {
		return errors.New("service: chatgpt tokens are too large to store")
	}
	for i, p := range parts {
		if err := t.secrets.Set(chatgptPartName(i+1), p); err != nil {
			return fmt.Errorf("service: write chatgpt tokens: %w", err)
		}
	}
	head, err := json.Marshal(chatgptTokenHeader{V: chatgptTokenHeaderVer, Parts: len(parts)})
	if err != nil {
		return fmt.Errorf("service: encode chatgpt tokens: %w", err)
	}
	if err := t.secrets.Set(platform.SecretChatGPT, string(head)); err != nil {
		return fmt.Errorf("service: write chatgpt tokens: %w", err)
	}
	// 余った古い parts を、最初に無かったところまで消す。掃除の失敗は記録の読み出しに影響しない（見出しが範囲を決める）。
	for i := len(parts) + 1; i <= chatgptTokenMaxParts; i++ {
		if err := t.secrets.Delete(chatgptPartName(i)); err != nil {
			break
		}
	}
	return nil
}

// delete は見出しと、すべての parts を消す（見出しが先。以後の読み出しは「なし」）。無いものは無視し、最初の失敗を返す。
func (t chatgptTokenStore) delete() error {
	var first error
	note := func(err error) {
		if err != nil && !errors.Is(err, platform.ErrNotFound) && first == nil {
			first = fmt.Errorf("service: delete chatgpt tokens: %w", err)
		}
	}
	note(t.secrets.Delete(platform.SecretChatGPT))
	for i := 1; i <= chatgptTokenMaxParts; i++ {
		note(t.secrets.Delete(chatgptPartName(i)))
	}
	return first
}

// splitUTF8 は s を、文字の途中で切らずに limit バイト以下ずつに分ける（s が空でなければ 1 つ以上）。
func splitUTF8(s string, limit int) []string {
	var out []string
	for len(s) > limit {
		cut := limit
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		if cut == 0 { // 不正な UTF-8 でも進める
			cut = limit
		}
		out = append(out, s[:cut])
		s = s[cut:]
	}
	if s != "" || len(out) == 0 {
		out = append(out, s)
	}
	return out
}
