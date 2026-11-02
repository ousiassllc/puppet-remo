package typesafe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type chooser struct {
	key      func(ctx context.Context) (string, error)
	endpoint string
	timeout  time.Duration
	maxBody  int
	client   *http.Client
}

var _ Chooser = (*chooser)(nil)

// New は Chooser を作る。通信も goroutine も、Choose が呼ばれるまで起こさない。
// key は Choose のたびに呼ぶ（実体は SecretStore.Get(SecretTypeSafe)）。キーは保持せず、ログにもエラーにも出さない。
func New(key func(ctx context.Context) (string, error), cfg Config) Chooser {
	return newChooser(key, cfg, newTransport())
}

func newChooser(key func(ctx context.Context) (string, error), cfg Config, rt http.RoundTripper) *chooser {
	if cfg.Endpoint == "" {
		cfg.Endpoint = defaultEndpoint
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.MaxBody <= 0 {
		cfg.MaxBody = defaultMaxBody
	}
	return &chooser{
		key:      key,
		endpoint: cfg.Endpoint,
		timeout:  cfg.Timeout,
		maxBody:  cfg.MaxBody,
		client: &http.Client{
			Transport: rt,
			// 送信先は api.typesafe.ai だけ（NFR-S18）。リダイレクトは辿らず、3xx は想定外の状態として扱う。
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

func (c *chooser) Choose(ctx context.Context, req ChoiceRequest) (ChoiceResult, error) {
	if err := ctx.Err(); err != nil {
		return ChoiceResult{}, err
	}
	body, allowed, err := marshalRequest(req)
	if err != nil {
		return ChoiceResult{}, err
	}
	// 本文の上限は送信前に確かめる（NFR-S18）。超過は縮退（OpenAI へ引き継ぎ）で扱う。
	if len(body) > c.maxBody {
		return ChoiceResult{}, fmt.Errorf("%w: request body %d bytes exceeds %d", ErrUnavailable, len(body), c.maxBody)
	}

	key, err := c.key(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ChoiceResult{}, ctx.Err()
		}
		return ChoiceResult{}, fmt.Errorf("%w: read API key: %w", ErrUnavailable, err)
	}
	if !validKey(key) {
		return ChoiceResult{}, fmt.Errorf("%w: API key is empty or malformed", ErrUnauthorized)
	}

	// 1 回の問い合わせの期限。再試行はしない（NFR-S18）。
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return ChoiceResult{}, fmt.Errorf("%w: build request", ErrUnavailable)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+key)

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return ChoiceResult{}, c.transportError(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return ChoiceResult{}, fmt.Errorf("%w: HTTP 401", ErrUnauthorized)
	default:
		// 429・5xx に加え、400・403・3xx などの想定外の状態も、そのステップを OpenAI に任せる
		// （応答本文は誤りの文面に含めない）。
		return ChoiceResult{}, fmt.Errorf("%w: HTTP %d", ErrUnavailable, resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return ChoiceResult{}, c.transportError(ctx, err)
	}
	return unmarshalResponse(data, allowed)
}

// transportError は通信の失敗を分類する。呼び出し側の ctx が先に終わったなら、そのエラーをそのまま返す
// （キャンセルは縮退ではない）。それ以外（自前の期限・ネットワーク）は ErrUnavailable。
func (c *chooser) transportError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return fmt.Errorf("%w: %w", ErrUnavailable, err)
}

// validKey はキーを Authorization ヘッダーに載せてよいか。空・空白・制御文字を含むものは送らない
// （net/http のヘッダー検査の失敗に頼らない）。
func validKey(key string) bool {
	if key == "" {
		return false
	}
	for i := range len(key) {
		if b := key[i]; b <= ' ' || b == 0x7f {
			return false
		}
	}
	return true
}

// redactTransport は RoundTrip が返すエラーの文面から Authorization のキーを伏せる。
// 標準の net/http はキーをエラーに入れないが、差し替えられたトランスポートや将来の変更に備えた最後の防御。
type redactTransport struct{ inner http.RoundTripper }

func newTransport() http.RoundTripper {
	return redactTransport{inner: http.DefaultTransport.(*http.Transport).Clone()}
}

func (t redactTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.inner.RoundTrip(req)
	if err != nil {
		if key := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer "); key != "" {
			if msg := err.Error(); strings.Contains(msg, key) {
				return nil, errors.New(strings.ReplaceAll(msg, key, "[REDACTED]"))
			}
		}
	}
	return resp, err
}
