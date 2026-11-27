package service

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// このファイルは ID トークンの検証（FR-G28 (6)・NFR-S21 (4)）: OpenAI の公開 JWKS による RS256 の署名、iss・aud（発行された client_id）・
// exp・nonce。標準ライブラリだけで書く。トークンの中身（クレームの値）はエラー・ログに出さない。

var errChatGPTIDToken = errors.New("service: the chatgpt id token is not valid")

const (
	chatgptJWKSTTL        = time.Hour
	chatgptJWKSRetryAfter = time.Minute // 知らない kid で取り直すのは、前回の取得からこれだけ空いたとき
	chatgptIDTokenLeeway  = time.Minute
	chatgptMinRSABits     = 2048
	chatgptMaxBody        = 1 << 20
)

// chatgptIDClaims は検証済みの ID トークンの、アプリが使う事実だけ。
type chatgptIDClaims struct {
	Subject string
	Email   string
}

type chatgptJWKSCache struct {
	mu      sync.Mutex
	keys    map[string]*rsa.PublicKey
	fetched time.Time
}

// verifyIDToken は raw の署名・iss・aud・exp・nonce を検証し、クレームを返す。
func (s *ChatGPTAuthService) verifyIDToken(ctx context.Context, raw, clientID, nonce string) (chatgptIDClaims, error) {
	invalid := func() (chatgptIDClaims, error) { return chatgptIDClaims{}, errChatGPTIDToken }
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return invalid()
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if !decodeJWTPart(parts[0], &hdr) || hdr.Alg != "RS256" { // alg を信用して切り替えない（RS256 だけ）
		return invalid()
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return invalid()
	}
	key, err := s.signingKey(ctx, hdr.Kid)
	if err != nil {
		if errors.Is(err, errChatGPTIDToken) {
			return invalid()
		}
		return chatgptIDClaims{}, err // JWKS を取れない（ネットワークなど）。トークンの不正とは区別する
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig) != nil {
		return invalid()
	}

	var cl struct {
		Iss   string          `json:"iss"`
		Aud   json.RawMessage `json:"aud"`
		Exp   *float64        `json:"exp"`
		Nbf   *float64        `json:"nbf"`
		Nonce string          `json:"nonce"`
		Sub   string          `json:"sub"`
		Email string          `json:"email"`
	}
	if !decodeJWTPart(parts[1], &cl) {
		return invalid()
	}
	now := s.cfg.Clock.Now()
	switch {
	case cl.Iss != s.issuer():
		return invalid()
	case !audienceHas(cl.Aud, clientID):
		return invalid()
	case cl.Exp == nil || !now.Before(unixSeconds(*cl.Exp).Add(chatgptIDTokenLeeway)):
		return invalid()
	case cl.Nbf != nil && now.Add(chatgptIDTokenLeeway).Before(unixSeconds(*cl.Nbf)):
		return invalid()
	case nonce == "" || subtle.ConstantTimeCompare([]byte(cl.Nonce), []byte(nonce)) != 1:
		return invalid()
	case cl.Sub == "":
		return invalid()
	}
	return chatgptIDClaims{Subject: cl.Sub, Email: cl.Email}, nil
}

func unixSeconds(f float64) time.Time { return time.Unix(int64(f), 0) }

func decodeJWTPart(seg string, v any) bool {
	b, err := base64.RawURLEncoding.DecodeString(seg)
	return err == nil && json.Unmarshal(b, v) == nil
}

// audienceHas は aud（文字列または文字列の配列）が want を含むか。
func audienceHas(aud json.RawMessage, want string) bool {
	var one string
	if json.Unmarshal(aud, &one) == nil {
		return one == want
	}
	var many []string
	if json.Unmarshal(aud, &many) == nil {
		for _, a := range many {
			if a == want {
				return true
			}
		}
	}
	return false
}

// signingKey は kid の公開鍵を返す。キャッシュに無ければ（取得から一定時間たっていれば）JWKS を取り直す。
// kid が無いトークンは、JWKS の鍵が 1 つだけのときにだけその鍵で検証する。
func (s *ChatGPTAuthService) signingKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	c := &s.jwks
	c.mu.Lock()
	defer c.mu.Unlock()
	now := s.cfg.Clock.Now()
	pick := func() (*rsa.PublicKey, bool) {
		if kid == "" && len(c.keys) == 1 {
			for _, k := range c.keys {
				return k, true
			}
		}
		k, ok := c.keys[kid]
		return k, ok && kid != ""
	}
	fresh := c.keys != nil && now.Sub(c.fetched) < chatgptJWKSTTL
	if fresh {
		if k, ok := pick(); ok {
			return k, nil
		}
		if now.Sub(c.fetched) < chatgptJWKSRetryAfter {
			return nil, errChatGPTIDToken
		}
	}
	keys, err := s.fetchJWKS(ctx)
	if err != nil {
		return nil, err
	}
	c.keys, c.fetched = keys, now
	if k, ok := pick(); ok {
		return k, nil
	}
	return nil, errChatGPTIDToken
}

// jwksURL は ID トークンの検証鍵の取得先。探索文書（openid-configuration）の jwks_uri を使う。jwks_uri が発行者の配下で
// なければ鍵を取りに行かず、ID トークンの検証の失敗にする。探索に失敗したか jwks_uri が無いときは、発行者の既定の
// /.well-known/jwks.json を使う。
func (s *ChatGPTAuthService) jwksURL(ctx context.Context) (string, error) {
	doc, err := s.doHTTP(ctx, http.MethodGet, s.authURL(chatgptDiscoveryPath), nil, "", chatgptJWKSTimeout)
	var d struct {
		JWKS string `json:"jwks_uri"`
	}
	if err != nil || json.Unmarshal(doc, &d) != nil || d.JWKS == "" {
		return s.authURL("/.well-known/jwks.json"), nil
	}
	if !strings.HasPrefix(d.JWKS, s.issuer()+"/") {
		return "", errChatGPTIDToken
	}
	return d.JWKS, nil
}

func (s *ChatGPTAuthService) fetchJWKS(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	src, err := s.jwksURL(ctx)
	if err != nil {
		return nil, err
	}
	body, err := s.doHTTP(ctx, http.MethodGet, src, nil, "", chatgptJWKSTimeout)
	if err != nil {
		return nil, err
	}
	var set struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			Use string `json:"use"`
			Alg string `json:"alg"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if json.Unmarshal(body, &set) != nil {
		return nil, errChatGPTIDToken
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "RSA" || (k.Use != "" && k.Use != "sig") || (k.Alg != "" && k.Alg != "RS256") {
			continue
		}
		n, e := b64BigInt(k.N), b64BigInt(k.E)
		if n == nil || e == nil || !e.IsInt64() || e.Int64() < 3 || e.Int64() > 1<<31-1 || n.BitLen() < chatgptMinRSABits {
			continue
		}
		keys[k.Kid] = &rsa.PublicKey{N: n, E: int(e.Int64())}
	}
	if len(keys) == 0 {
		return nil, errChatGPTIDToken
	}
	return keys, nil
}

func b64BigInt(s string) *big.Int {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
	if err != nil || len(raw) == 0 {
		return nil
	}
	return new(big.Int).SetBytes(raw)
}
