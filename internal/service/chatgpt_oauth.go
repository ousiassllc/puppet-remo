package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// このファイルは Sign in with ChatGPT の OAuth の部品（FR-G28・G29・NFR-S21）: PKCE・認可の URL・コードの交換・更新・失効・HTTP。
// 送信先は認可サーバー（AuthBaseURL。既定 https://auth.openai.com）と API（APIBaseURL。既定 https://api.openai.com/v1）だけ。
// エラーに URL・本文（トークン・コードを含みうる）を入れない。

const (
	chatgptDefaultAuthBase = "https://auth.openai.com"
	chatgptDefaultAPIBase  = "https://api.openai.com/v1"

	chatgptDynamicClientID = "dynamic_agent_client"
	chatgptAgentNameHint   = "puppet-remo"
	chatgptResource        = "https://api.openai.com/v1"
	// chatgptScopeUseDirect は ChatGPT のプランを直接使う scope（付与されなければ使えない。FR-G28 (6)）。
	chatgptScopeUseDirect = "chatgpt.tokens.use.direct"
	chatgptScope          = "openid profile email offline_access resource.invoke " + chatgptScopeUseDirect

	// ChatGPTUsageURL は使用量の設定（開く先はこの 1 つだけ。トークン・クエリを付けない。NFR-S21）。
	ChatGPTUsageURL = "https://chatgpt.com/settings/usage"

	chatgptRefreshTimeout   = 10 * time.Second // FR-G29 (2)
	chatgptExchangeTimeout  = 15 * time.Second
	chatgptRevokeTimeout    = 5 * time.Second // FR-G29 (6) ②（1 回）
	chatgptJWKSTimeout      = 10 * time.Second
	chatgptModelsTimeout    = 20 * time.Second
	chatgptRefreshEarly     = 5 * time.Minute // 残りがこれ以下なら更新する
	chatgptDefaultExpiresIn = time.Hour
)

// 更新が「このトークンは使えない」と返すコード（FR-G29 (3)）。
var chatgptRefreshDeadCodes = map[string]bool{
	"invalid_grant": true, "invalid_refresh_token": true, "token_expired": true,
	"refresh_token_expired": true, "refresh_token_invalidated": true, "refresh_token_reused": true,
}

// ポリシー・資格の拒否（Codex の経路へ案内する。FR-G30 (5)・#247）。
func chatgptIsPolicyDeniedCode(code string) bool {
	return code == "3p_delegated_access_policy_denied" || code == "subscription_sharing_user_not_eligible"
}

// chatgptHTTPError は認可サーバー・API の HTTP エラー。Code は応答の error（検証済みの短い識別子だけ。本文は持たない）。
type chatgptHTTPError struct {
	Status int
	Code   string
}

func (e *chatgptHTTPError) Error() string {
	if e.Code == "" {
		return "service: chatgpt endpoint returned HTTP " + strconv.Itoa(e.Status)
	}
	return "service: chatgpt endpoint returned HTTP " + strconv.Itoa(e.Status) + " (" + e.Code + ")"
}

// chatgptNetError は通信の失敗（URL を含まない）。
type chatgptNetError struct{ err error }

func (e *chatgptNetError) Error() string {
	return "service: chatgpt endpoint is unreachable: " + e.err.Error()
}
func (e *chatgptNetError) Unwrap() error { return e.err }

// stripURLError は *url.Error の URL を落として、内側のエラーだけを返す。
func stripURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err
	}
	return err
}

func (s *ChatGPTAuthService) authURL(path string) string { return s.authBase + path }
func (s *ChatGPTAuthService) issuer() string             { return s.authBase }

// doHTTP は 1 回の要求を送り、2xx の本文を返す。form が非 nil なら POST のフォーム。bearer が空でなければ Authorization を付ける。
// 2xx 以外は *chatgptHTTPError、通信の失敗は *chatgptNetError（どちらも URL・本文を含まない）。
func (s *ChatGPTAuthService) doHTTP(ctx context.Context, method, rawURL string, form url.Values, bearer string, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, errors.New("service: build chatgpt request")
	}
	req.Header.Set("Accept", "application/json")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := s.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, &chatgptNetError{err: stripURLError(err)}
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(res.Body, chatgptMaxBody))
	if err != nil {
		return nil, &chatgptNetError{err: stripURLError(err)}
	}
	if res.StatusCode/100 != 2 {
		return nil, &chatgptHTTPError{Status: res.StatusCode, Code: chatgptErrorCode(data)}
	}
	return data, nil
}

// chatgptErrorCode は応答の本文から error の識別子（{"error":"x"} か {"error":{"code":"x"}}）を取り出す。
// 英小文字・数字・_・. 64 文字以内だけを認め、それ以外（説明文・値）は空にする。
func chatgptErrorCode(body []byte) string {
	var v struct {
		Error json.RawMessage `json:"error"`
		Code  string          `json:"code"`
	}
	if json.Unmarshal(body, &v) != nil {
		return ""
	}
	code := v.Code
	var str string
	var obj struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(v.Error, &str) == nil {
		code = str
	} else if json.Unmarshal(v.Error, &obj) == nil && obj.Code != "" {
		code = obj.Code
	}
	if len(code) == 0 || len(code) > 64 {
		return ""
	}
	for _, r := range code {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == '-') {
			return ""
		}
	}
	return code
}

// ---- PKCE・乱数 ----

func randomURLToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("service: random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// newHostID は ext_agent_host_id（urn:uuid: + UUIDv4）。
func newHostID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("service: random: %w", err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("urn:uuid:%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// ---- 認可の URL ----

type chatgptAuthorizeParams struct {
	ClientID     string // 初回は dynamic_agent_client
	NewClient    bool   // 初回の登録（agent_name_hint を付ける）
	HostID       string
	LoginHint    string
	RedirectURI  string
	State, Nonce string
	Challenge    string
	Consent      bool // prompt=consent（scope が付与されなかったあとの「もう一度許可する」）
}

func (s *ChatGPTAuthService) authorizeURL(p chatgptAuthorizeParams) string {
	q := url.Values{}
	q.Set("client_id", p.ClientID)
	if p.NewClient {
		q.Set("agent_name_hint", chatgptAgentNameHint)
	}
	q.Set("ext_agent_host_id", p.HostID)
	if p.LoginHint != "" {
		q.Set("login_hint", p.LoginHint)
	}
	q.Set("response_type", "code")
	q.Set("redirect_uri", p.RedirectURI)
	q.Set("scope", chatgptScope)
	q.Set("resource", chatgptResource)
	q.Set("state", p.State)
	q.Set("nonce", p.Nonce)
	q.Set("code_challenge", p.Challenge)
	q.Set("code_challenge_method", "S256")
	if p.Consent {
		q.Set("prompt", "consent")
	}
	return s.authURL("/api/accounts/authorize") + "?" + q.Encode()
}

// ---- トークンの交換・更新・失効 ----

type chatgptTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
}

func decodeChatGPTTokens(data []byte) (chatgptTokenResponse, error) {
	var t chatgptTokenResponse
	if json.Unmarshal(data, &t) != nil || t.AccessToken == "" {
		return chatgptTokenResponse{}, &chatgptNetError{err: errors.New("the token endpoint answered with no access token")}
	}
	return t, nil
}

func (s *ChatGPTAuthService) postTokenEndpoint(ctx context.Context, form url.Values, timeout time.Duration) (chatgptTokenResponse, error) {
	data, err := s.doHTTP(ctx, http.MethodPost, s.authURL("/api/accounts/oauth/token"), form, "", timeout)
	if err != nil {
		return chatgptTokenResponse{}, err
	}
	return decodeChatGPTTokens(data)
}

func (s *ChatGPTAuthService) exchangeCode(ctx context.Context, clientID, code, verifier, redirectURI string) (chatgptTokenResponse, error) {
	return s.postTokenEndpoint(ctx, url.Values{
		"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {code},
		"code_verifier": {verifier}, "redirect_uri": {redirectURI}, "resource": {chatgptResource},
	}, chatgptExchangeTimeout)
}

// refreshTokens はリフレッシュトークンを新しいトークンに換える（scope は送らない。FR-G29 (2)）。
func (s *ChatGPTAuthService) refreshTokens(ctx context.Context, clientID, refreshToken string) (chatgptTokenResponse, error) {
	return s.postTokenEndpoint(ctx, url.Values{
		"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {refreshToken}, "resource": {chatgptResource},
	}, chatgptRefreshTimeout)
}

// revokeOnce は失効を 1 回試す（探索 → revocation_endpoint へのフォーム）。成功（2xx）は nil。
// 探索の revocation_endpoint が発行者の配下でなければ、リフレッシュトークンを送らずに失敗にする（permanent = true）。
func (s *ChatGPTAuthService) revokeOnce(ctx context.Context, clientID, refreshToken string) (permanent bool, err error) {
	doc, err := s.doHTTP(ctx, http.MethodGet, s.authURL("/.well-known/openid-configuration"), nil, "", chatgptRevokeTimeout)
	if err != nil {
		return false, err
	}
	var d struct {
		Revocation string `json:"revocation_endpoint"`
	}
	if json.Unmarshal(doc, &d) != nil || !strings.HasPrefix(d.Revocation, s.issuer()+"/") {
		return true, errors.New("service: no trusted revocation endpoint")
	}
	_, err = s.doHTTP(ctx, http.MethodPost, d.Revocation, url.Values{
		"token": {refreshToken}, "token_type_hint": {"refresh_token"}, "client_id": {clientID},
	}, "", chatgptRevokeTimeout)
	if err != nil {
		var he *chatgptHTTPError
		// 4xx（429 を除く）は繰り返しても変わらない。5xx・通信の失敗・429 は再試行する。
		if errors.As(err, &he) && he.Status/100 == 4 && he.Status != http.StatusTooManyRequests {
			return true, err
		}
	}
	return false, err
}
