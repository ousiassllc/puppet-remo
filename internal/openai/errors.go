package openai

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"
)

// ErrUnauthorized は OpenAI が HTTP 401（API キーが無効）を返したことを表す。*APIError（Status 401）が errors.Is で一致する。
// agent はこれで GUI へ通知し、エージェントを無効に戻す（NFR-S16）。メッセージにキーは含まない。
var ErrUnauthorized = errors.New("openai: unauthorized")

// APIError は OpenAI が返した HTTP エラー（status >= 400）。API キー・リクエストのヘッダー・本文は持たない。
// agent は errors.Is(err, ErrUnauthorized) で 401（キー無効）を見分ける（NFR-S16）。
type APIError struct {
	Status  int    // HTTP ステータス
	Type    string // error.type（例: invalid_request_error）
	Code    string // error.code（無ければ空）
	Message string // error.message（キーの断片が入りうる 401 では空。長いものは切り詰める）
}

// Is は Status が 401 の APIError を ErrUnauthorized と同一視する。
func (e *APIError) Is(target error) bool {
	return target == ErrUnauthorized && e.Status == http.StatusUnauthorized
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "openai: HTTP %d", e.Status)
	if e.Type != "" {
		b.WriteString(" type=" + e.Type)
	}
	if e.Code != "" {
		b.WriteString(" code=" + e.Code)
	}
	if e.Message != "" {
		b.WriteString(": " + e.Message)
	}
	return b.String()
}

// maxMessageRunes はエラーメッセージの上限（ログ・監査へ載るため）。
const maxMessageRunes = 300

// maxErrorBody は読むエラー本文の上限。
const maxErrorBody = 64 << 10

// newAPIError はエラー応答（本文は SDK が読み戻してある）から APIError を作る。key は本文から伏せる。
func newAPIError(res *http.Response, key string) *APIError {
	e := &APIError{Status: res.StatusCode}
	if res.Body == nil {
		return e
	}
	body, _ := io.ReadAll(io.LimitReader(res.Body, maxErrorBody))
	var env struct {
		Error struct {
			Type    string          `json:"type"`
			Code    json.RawMessage `json:"code"`
			Message string          `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &env) != nil {
		return e
	}
	e.Type = redact(env.Error.Type, key)
	var code string
	if json.Unmarshal(env.Error.Code, &code) != nil {
		code = strings.Trim(string(env.Error.Code), `"`)
		if code == "null" {
			code = ""
		}
	}
	e.Code = redact(code, key)
	// 401 のメッセージは「Incorrect API key provided: sk-…」のようにキーの一部を含む。
	if res.StatusCode != http.StatusUnauthorized {
		e.Message = truncate(redact(env.Error.Message, key), maxMessageRunes)
	}
	return e
}

// redact は s に含まれる key を伏せる。
func redact(s, key string) string {
	if key == "" {
		return s
	}
	return strings.ReplaceAll(s, key, "[redacted]")
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// redactedError は HTTP 以外の失敗（タイムアウト・通信エラー・取り消し）。元のエラーを Unwrap で辿れる
// （errors.Is(err, context.DeadlineExceeded) など）が、Error() の文言はキーを伏せたもの。
type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }

func newRequestError(err error, key string) error {
	return &redactedError{msg: "openai: request failed: " + redact(err.Error(), key), err: err}
}

// safeTransport は Authorization ヘッダーの値（Bearer キー）を、トランスポートが返すエラーの文言から伏せる RoundTripper。
// リクエスト・ヘッダーはログにも出さない。
type safeTransport struct{ base http.RoundTripper }

func (t safeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	res, err := t.base.RoundTrip(req)
	if err != nil {
		auth := req.Header.Get("Authorization")
		msg := redact(err.Error(), auth)
		msg = redact(msg, strings.TrimPrefix(auth, "Bearer "))
		return nil, &redactedError{msg: msg, err: err}
	}
	return res, nil
}
