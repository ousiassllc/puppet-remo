package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/openai/openai-go/v3/responses"
)

// このファイルは接続方式 chatgpt（公式の Sign in with ChatGPT。FR-G26）の Model。
// OAuth のアクセストークンを Bearer にして Responses API（POST /v1/responses）を、関数ツール（namespace desktop。FR-G27）つきで呼ぶ。
// SDK は使わない: この経路は stream: true が必須で、送る項目も絞られるため、要求は手で組み立てる（送らない項目が混ざらないように）。

// defaultChatGPTBaseURL は公式の経路の送信先。api.openai.com 以外へトークンを送らない（FR-G26）。
const defaultChatGPTBaseURL = "https://api.openai.com/v1"

// ChatGPTConfig は NewChatGPT・ListChatGPTModels の設定。
type ChatGPTConfig struct {
	// Timeout は 1 回の呼び出し（接続から終端のイベントまでの全体）のタイムアウト。0 なら DefaultTimeout（60 秒。NFR-S16）。
	Timeout time.Duration

	// 以下はテスト専用の差し替え口で、利用者の設定項目ではない（config・GUI には出さない）。

	// BaseURL は API のベース URL（/responses・/models の前まで）。空なら https://api.openai.com/v1。
	BaseURL string
	// Transport は下位の RoundTripper。nil なら http.DefaultTransport。Authorization を伏せる safeTransport で包まれる。
	Transport http.RoundTripper
}

// ChatGPT のプラン経路の失敗コード（原資料。FR-G29・G30）。
const (
	codeUsageLimit    = "subscription_sharing_usage_limit_exceeded"
	codeInvalidUser   = "subscription_sharing_invalid_user"
	codePolicyDenied  = "3p_delegated_access_policy_denied"
	codeUserNotEligib = "subscription_sharing_user_not_eligible"
)

// maxModelsBody は読むモデル一覧の応答の上限。
const maxModelsBody = 4 << 20

// NewChatGPT は ChatGPT のプラン（公式の SIWC）の Model を作る。token は Step のたびに呼ばれる（トークンを保持しない。
// 実体は ChatGPTAuth の「必要なら更新したアクセストークン」）。トークンはどのエラー・ログにも含まれない。
//
// 要求は毎回 store: false・stream: true で、previous_response_id は使わず、送る項目は model・instructions・input・tools・store・stream だけ
// （reasoning・include・parallel_tool_calls・tool_choice・temperature・max_output_tokens・metadata などは送らない。FR-G26）。
// 応答は終端のイベントまで読み、response.completed だけを成功とする。それ以外（response.failed・response.incomplete・error・
// 終端なしの切断）は失敗で、途中までの function_call は返さない。再試行しない（NFR-S16）。
//
// 失敗は errors.Is で見分ける: ErrUsageLimit（429 / response.failed の subscription_sharing_usage_limit_exceeded）、
// ErrPlanUnauthorized（401・subscription_sharing_invalid_user）、ErrPolicyDenied（3p_delegated_access_policy_denied・
// subscription_sharing_user_not_eligible）。ErrUnauthorized（API キー経路の 401）とは一致しない。errors.As で *APIError（code・param）も取れる。
// それ以外の HTTP の失敗は *APIError、通信・タイムアウトは元のエラーを Unwrap できる通常のエラー、写像できない関数の呼び出しは
// domain.ErrUnsupportedAction。
func NewChatGPT(token func(ctx context.Context) (string, error), cfg ChatGPTConfig) Model {
	return &chatgptClient{token: token, c: newChatGPTHTTP(cfg)}
}

// PlanModelInfo は GET /v1/models の 1 件。
type PlanModelInfo struct {
	Slug        string // model に渡す値
	DisplayName string // 選択肢に出す名前（無ければ Slug）
}

// ListChatGPTModels は同じ Bearer で GET /v1/models を呼び、visibility が "list" のものだけを、サーバーの順のまま返す（FR-G26）。
// 応答は data[] の slug・display_name を想定し、slug が無ければ id を使う。失敗の分類は NewChatGPT と同じ。
func ListChatGPTModels(ctx context.Context, token string, cfg ChatGPTConfig) ([]PlanModelInfo, error) {
	h := newChatGPTHTTP(cfg)
	if token == "" {
		return nil, errors.New("openai: chatgpt access token is empty")
	}
	ctx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()
	res, err := h.do(ctx, token, http.MethodGet, "/models", nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(res.Body, maxModelsBody))
	if err != nil {
		return nil, newRequestError(err, token)
	}
	var list struct {
		Data   []modelEntry `json:"data"`
		Models []modelEntry `json:"models"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, errors.New("openai: invalid models response")
	}
	entries := list.Data
	if len(entries) == 0 {
		entries = list.Models
	}
	out := make([]PlanModelInfo, 0, len(entries))
	for _, e := range entries {
		if e.Visibility != "list" {
			continue
		}
		slug := e.Slug
		if slug == "" {
			slug = e.ID
		}
		if slug == "" {
			continue
		}
		name := e.DisplayName
		if name == "" {
			name = slug
		}
		out = append(out, PlanModelInfo{Slug: slug, DisplayName: name})
	}
	return out, nil
}

type modelEntry struct {
	Slug        string `json:"slug"`
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Visibility  string `json:"visibility"`
}

// chatgptHTTP は ChatGPT 経路の HTTP 呼び出し（要求の送信とエラーの分類）。
type chatgptHTTP struct {
	base    string
	timeout time.Duration
	client  *http.Client
}

func newChatGPTHTTP(cfg ChatGPTConfig) *chatgptHTTP {
	h := &chatgptHTTP{base: strings.TrimRight(cfg.BaseURL, "/"), timeout: cfg.Timeout}
	if h.base == "" {
		h.base = defaultChatGPTBaseURL
	}
	if h.timeout <= 0 {
		h.timeout = DefaultTimeout
	}
	rt := cfg.Transport
	if rt == nil {
		rt = http.DefaultTransport
	}
	h.client = &http.Client{
		Transport: safeTransport{base: rt},
		// リダイレクトは辿らない（トークンを別のホストへ送らない）。3xx は HTTP の失敗として扱う。
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return h
}

// do は要求を送り、200 だけを返す。それ以外の HTTP は分類したエラー、通信の失敗は伏せたエラーにする（どちらもトークンを含まない）。
func (h *chatgptHTTP) do(ctx context.Context, token, method, path string, body []byte) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, h.base+path, rd)
	if err != nil {
		return nil, errors.New("openai: invalid request url")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	res, err := h.client.Do(req)
	if err != nil {
		return nil, newRequestError(err, token)
	}
	if res.StatusCode != http.StatusOK {
		defer func() { _ = res.Body.Close() }()
		return nil, classifyPlan(newAPIError(res, token))
	}
	return res, nil
}

type chatgptClient struct {
	token func(ctx context.Context) (string, error)
	c     *chatgptHTTP
}

func (c *chatgptClient) Step(ctx context.Context, in StepInput) (StepOutput, error) {
	if in.Model == "" || in.History == nil {
		return StepOutput{}, errors.New("openai: model and history are required")
	}
	input, err := in.History.input() // 出力の欠けた・重複した call_id は、送る前に拒否する
	if err != nil {
		return StepOutput{}, err
	}
	token, err := c.token(ctx)
	if err != nil {
		return StepOutput{}, fmt.Errorf("openai: read chatgpt token: %w", err)
	}
	if token == "" {
		return StepOutput{}, errors.New("openai: chatgpt access token is empty")
	}
	body, err := json.Marshal(chatgptRequest{
		Model:        in.Model,
		Instructions: in.Instructions,
		Input:        input,
		Tools:        []any{desktopNamespaceTool()},
		Store:        false,
		Stream:       true,
	})
	if err != nil {
		return StepOutput{}, fmt.Errorf("openai: encode request: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, c.c.timeout)
	defer cancel()
	res, err := c.c.do(ctx, token, http.MethodPost, "/responses", body)
	if err != nil {
		return StepOutput{}, err
	}
	defer func() { _ = res.Body.Close() }()
	return readChatGPTStream(res.Body, token)
}

// chatgptRequest は FR-G26 が許す項目だけの要求。これ以外の項目は型に無いので送られない。
type chatgptRequest struct {
	Model        string                       `json:"model"`
	Instructions string                       `json:"instructions"`
	Input        responses.ResponseInputParam `json:"input"`
	Tools        []any                        `json:"tools"`
	Store        bool                         `json:"store"`
	Stream       bool                         `json:"stream"`
}

// desktopNamespaceTool は DesktopTools を namespace desktop の関数ツールにまとめた tools の要素（FR-G26・G27）。
func desktopNamespaceTool() map[string]any {
	fns := make([]any, 0, len(DesktopTools))
	for _, t := range DesktopTools {
		fns = append(fns, map[string]any{
			"type":        "function",
			"name":        t.Name,
			"description": t.Description,
			"parameters":  t.Parameters,
			"strict":      false,
		})
	}
	return map[string]any{
		"type":        "namespace",
		"name":        DesktopNamespace,
		"description": DesktopNamespaceDescription,
		"tools":       fns,
	}
}

// planError は ChatGPT 経路の分類された失敗。errors.Is は分類（ErrUsageLimit など）にだけ一致し、
// *APIError の 401 が ErrUnauthorized（API キー経路の 401）に一致する振る舞いは引き継がない。errors.As で *APIError は取り出せる。
type planError struct {
	kind  error
	cause *APIError
}

func (e *planError) Error() string        { return e.cause.Error() }
func (e *planError) Is(target error) bool { return target == e.kind }

func (e *planError) As(target any) bool {
	if p, ok := target.(**APIError); ok {
		*p = e.cause
		return true
	}
	return false
}

// classifyPlan は ChatGPT 経路の APIError（HTTP の失敗、またはストリームの中のエラー）を分類する。どれにも当たらなければ *APIError のまま。
func classifyPlan(e *APIError) error {
	var kind error
	switch {
	case e.Code == codeUsageLimit:
		kind = ErrUsageLimit
	case e.Status == http.StatusUnauthorized || e.Code == codeInvalidUser:
		kind = ErrPlanUnauthorized
	case e.Code == codePolicyDenied || e.Code == codeUserNotEligib:
		kind = ErrPolicyDenied
	default:
		return e
	}
	return &planError{kind: kind, cause: e}
}
