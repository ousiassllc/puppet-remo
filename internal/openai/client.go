package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
)

// DefaultTimeout は 1 ステップ（Responses API 呼び出し 1 回）のタイムアウト（NFR-S16）。
const DefaultTimeout = 60 * time.Second

// Config は New の設定。
type Config struct {
	// Timeout は Step 1 回のタイムアウト。0 なら DefaultTimeout（60 秒）。
	Timeout time.Duration
	// Include は Responses API の include（例: "reasoning.encrypted_content"）。既定は空。
	// store: false の継続で何が要るかは実 API でまだ確認していない（docs §12.2）。確認後に既定を決める。
	Include []string

	// 以下はテスト専用の差し替え口で、利用者の設定項目ではない（config・GUI には出さない）。

	// BaseURL は API のベース URL。空なら OpenAI の本番（SDK の既定。環境変数 OPENAI_BASE_URL は読まない）。
	BaseURL string
	// Transport は下位の RoundTripper。nil なら http.DefaultTransport。Authorization を伏せる safeTransport で包まれる。
	Transport http.RoundTripper
}

// New は OpenAI の Model を作る。key は Step のたびに呼ばれる（キーを保持しない。実体は SecretStore の puppet-remo/openai）。
// SDK の自動再試行は無効にし、環境変数（OPENAI_API_KEY など）は読まない。
func New(key func(ctx context.Context) (string, error), cfg Config) Model {
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	base := cfg.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	opts := []option.RequestOption{
		option.WithEnvironmentProduction(),
		option.WithHTTPClient(&http.Client{Transport: safeTransport{base: base}}),
		option.WithMaxRetries(0),
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	return &client{key: key, cfg: cfg, svc: responses.NewResponseService(opts...)}
}

type client struct {
	key func(ctx context.Context) (string, error)
	cfg Config
	svc responses.ResponseService
}

func (c *client) Step(ctx context.Context, in StepInput) (StepOutput, error) {
	if in.Model == "" || in.History == nil {
		return StepOutput{}, errors.New("openai: model and history are required")
	}
	key, err := c.key(ctx)
	if err != nil {
		return StepOutput{}, fmt.Errorf("openai: read api key: %w", err)
	}
	if key == "" {
		return StepOutput{}, errors.New("openai: api key is empty")
	}

	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	var httpRes *http.Response
	res, err := c.svc.New(ctx, c.params(in), option.WithAPIKey(key), option.WithResponseInto(&httpRes))
	if err != nil {
		// SDK のエラー（*apierror.Error）は Request（Authorization ヘッダー）を抱えるので、そのまま返さない。
		if httpRes != nil && httpRes.StatusCode >= http.StatusBadRequest {
			return StepOutput{}, newAPIError(httpRes, key)
		}
		return StepOutput{}, newRequestError(err, key)
	}
	return parseResponse(res)
}

// params は要求を組み立てる。store: false 固定、previous_response_id なし（FR-G24）、ツールは GA の computer。
func (c *client) params(in StepInput) responses.ResponseNewParams {
	p := responses.ResponseNewParams{
		Model: in.Model,
		Input: responses.ResponseNewParamsInputUnion{OfInputItemList: in.History.input()},
		Store: param.NewOpt(false),
		Tools: []responses.ToolUnionParam{{OfComputer: &responses.ComputerToolParam{}}},
	}
	for _, inc := range c.cfg.Include {
		p.Include = append(p.Include, responses.ResponseIncludable(inc))
	}
	return p
}

// parseResponse は応答を StepOutput へ写す。出力アイテムは原文のまま（OutputItem.Raw）。
func parseResponse(res *responses.Response) (StepOutput, error) {
	switch res.Status {
	case responses.ResponseStatusFailed, responses.ResponseStatusCancelled, responses.ResponseStatusIncomplete:
		return StepOutput{}, fmt.Errorf("openai: response %s: %s", res.Status, responseProblem(res))
	}
	out := StepOutput{
		ResponseID: res.ID,
		Usage:      Usage{InputTokens: res.Usage.InputTokens, OutputTokens: res.Usage.OutputTokens},
	}
	var call *responses.ResponseComputerToolCall
	for _, item := range res.Output {
		out.Items = append(out.Items, OutputItem{Raw: []byte(item.RawJSON())})
		switch item.Type {
		case "computer_call":
			if call != nil {
				return StepOutput{}, errors.New("openai: response has more than one computer_call")
			}
			c := item.AsComputerCall()
			call = &c
		case "message":
			for _, part := range item.Content {
				switch part.Type {
				case "output_text":
					out.FinalText += part.Text
				case "refusal":
					out.FinalText += part.Refusal
				}
			}
		}
	}
	if call == nil {
		return out, nil
	}
	if call.CallID == "" {
		return StepOutput{}, errors.New("openai: computer_call without call_id")
	}
	out.FinalText = ""
	out.CallID = call.CallID
	for _, sc := range call.PendingSafetyChecks {
		out.SafetyChecks = append(out.SafetyChecks, SafetyCheck{ID: sc.ID, Code: sc.Code, Message: sc.Message})
	}
	actions := []responses.ComputerActionUnion(call.Actions)
	if len(actions) == 0 && call.Action.Type != "" {
		// 旧形式（単一の action）。
		var single responses.ComputerActionUnion
		if err := single.UnmarshalJSON([]byte(call.Action.RawJSON())); err != nil {
			return StepOutput{}, fmt.Errorf("openai: parse computer_call action: %w", err)
		}
		actions = []responses.ComputerActionUnion{single}
	}
	ops, err := mapActions(actions)
	if err != nil {
		return StepOutput{}, err
	}
	out.Actions = ops
	return out, nil
}

func responseProblem(res *responses.Response) string {
	if res.Error.Message != "" {
		return truncate(res.Error.Message, maxMessageRunes)
	}
	if res.IncompleteDetails.Reason != "" {
		return res.IncompleteDetails.Reason
	}
	return "no detail"
}
