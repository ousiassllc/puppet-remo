package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/repository"
	"github.com/ousiassllc/puppet-remo/internal/tailnet"
)

// web 層は tailnet・repository を import できない（scripts/check-imports.sh）ため、
// 連鎖が使う型は service の別名として公開する。
type (
	// Identity は WhoIs で解決した接続元。
	Identity = tailnet.Identity
	// WhoIs は接続元 IP の解決（接続元ごとに最大 10 秒キャッシュ。NFR-S05）。
	WhoIs = tailnet.WhoIs
	// TokenRecord は client_tokens の 1 行（ハッシュのみ。平文は持たない）。
	TokenRecord = repository.TokenRecord
)

// Feature は、リクエストが使うクライアント機能。許可リストの有効・無効の判定に使う。
type Feature int

const (
	// FeatureWeb は Web UI・WebSocket・JSON API・静的ファイル（clients.web_enabled）。
	FeatureWeb Feature = iota
	// FeatureMCP は /mcp（clients.mcp_enabled。Bearer トークンも必須）。
	FeatureMCP
)

// Request は Authorize が検査する 1 リクエスト分の情報。
type Request struct {
	RemoteAddr    string // http.Request.RemoteAddr（IP:port）
	Feature       Feature
	Authorization string // Authorization ヘッダーの値。FeatureMCP のときだけ使う
}

// Principal は認可を通ったリクエストの主体。
type Principal struct {
	Client   domain.Client
	Identity Identity
	Token    *repository.TokenRecord // FeatureMCP のときだけ。それ以外は nil
}

// ミドルウェア連鎖の 4〜6 で返す、API 仕様 §2 のコードに対応するエラー（errors.Is で判定する）。
var (
	ErrDeviceNotRegistered = domain.NewError(domain.CodeDeviceNotRegistered, "the device is not registered", nil)
	ErrClientDisabled      = domain.NewError(domain.CodeClientDisabled, "the client feature is disabled", nil)
	ErrUnauthorized        = domain.NewError(domain.CodeUnauthorized, "missing or invalid bearer token", nil)
	// ErrWhoIsFailed は WhoIs の失敗（whois_failed。fail closed）。
	ErrWhoIsFailed = tailnet.ErrWhoIsFailed
)

// DetailRetryAfterSec は rate_limited の *domain.Error.Detail に入れる、再試行までの秒数（int）のキー。
const DetailRetryAfterSec = "retry_after_sec"

// RetryAfter は err が rate_limited のとき、再試行までの待ち時間を返す。そうでなければ 0。
func RetryAfter(err error) time.Duration {
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodeRateLimited {
		return 0
	}
	if sec, ok := de.Detail[DetailRetryAfterSec].(int); ok {
		return time.Duration(sec) * time.Second
	}
	return 0
}

// AuditSink は認証系の監査イベントの書き込み先（consumer 側のインターフェース）。
// 実体は service の Audit（非同期キュー経由で repository.Audit.Append へ）。
// 呼び出しを遅延させず、トークン（平文・ハッシュ）は e に入れない。
type AuditSink interface {
	Record(ctx context.Context, e repository.AuditEntry)
}

// Authenticator はミドルウェア連鎖の 4〜6（WhoIs、許可リスト、Bearer）を 1 段ずつ提供する。
// キルスイッチ（3）は呼び出し側が先に判定する。
type Authenticator interface {
	// Identify は接続元を WhoIs で解決する（4）。失敗は whois_failed（fail closed）。
	Identify(ctx context.Context, remoteAddr string) (Identity, error)
	// CheckAllowlist は許可リストを照合する（5）。未登録・登録時と異なるユーザーは
	// device_not_registered（接続要求を作成・集約する）、機能が無効なら client_disabled。
	CheckAllowlist(ctx context.Context, remoteAddr string, id Identity, f Feature) (domain.Client, error)
	// VerifyBearer は /mcp の Bearer トークンを検証する（6）。authorization は Authorization ヘッダーの値。
	// 失敗は unauthorized、同一デバイスの失敗が 1 分 10 回を超えたら rate_limited。
	VerifyBearer(ctx context.Context, remoteAddr string, p Principal, authorization string) (*TokenRecord, error)
}

// Authz は認証・認可（FR-A03〜A09、NFR-S04/S05）。
type Authz interface {
	Authenticator
	// Authorize は連鎖の 4〜6 を順に実行する。FeatureMCP のときだけ Bearer を検証する。
	Authorize(ctx context.Context, r Request) (Principal, error)
	// IssueToken は clientID に Bearer トークンを発行し、平文を返す（平文を得られるのはこのときだけ）。
	// ttl が nil なら DefaultTokenTTL、0 なら無期限、負は invalid_argument。
	IssueToken(ctx context.Context, clientID, label string, ttl *time.Duration) (plaintext string, err error)
	// RevokeToken はトークン（ID）を失効する。失効済みなら元の失効時刻を保つ。
	RevokeToken(ctx context.Context, tokenID string) error
	// RecordConnectionRequest は未登録デバイスの接続要求を作る（レート制限付き）。
	// 拒否後 24 時間の抑止中、またはレート制限の超過分は、要求を作らず nil を返す。
	RecordConnectionRequest(ctx context.Context, id Identity, ip string) error
}

// AuthzConfig は NewAuthz の設定。Clock・IDs・Audit 以外は必須。
type AuthzConfig struct {
	WhoIs        tailnet.WhoIs
	Clients      repository.Clients
	Tokens       repository.Tokens
	ConnRequests repository.ConnRequests
	Audit        AuditSink // nil なら監査を書かない
	Clock        domain.Clock
	IDs          domain.IDGen
	Rand         io.Reader    // トークンの乱数源。nil なら crypto/rand
	Logger       *slog.Logger // nil なら slog.Default()
}

type authz struct {
	cfg AuthzConfig
	rnd io.Reader
	log *slog.Logger

	bearerFailures *windowLimiter // デバイス（stable node ID）ごと
	connRequests   *windowLimiter // 全体（キー ""）
}

// 認証失敗・接続要求のレート制限（NFR-S04、FR-A07）。
const (
	bearerFailureLimit    = 10
	connRequestLimit      = 10
	rateLimitWindow       = time.Minute
	connRequestPendingTTL = 24 * time.Hour // 保留の有効期限（FR-A06）
)

// NewAuthz は Authz を作る。
func NewAuthz(cfg AuthzConfig) (Authz, error) {
	switch {
	case cfg.WhoIs == nil:
		return nil, errors.New("service: authz requires WhoIs")
	case cfg.Clients == nil:
		return nil, errors.New("service: authz requires Clients")
	case cfg.Tokens == nil:
		return nil, errors.New("service: authz requires Tokens")
	case cfg.ConnRequests == nil:
		return nil, errors.New("service: authz requires ConnRequests")
	case cfg.Clock == nil:
		return nil, errors.New("service: authz requires Clock")
	case cfg.IDs == nil:
		return nil, errors.New("service: authz requires IDs")
	}
	a := &authz{
		cfg:            cfg,
		rnd:            cfg.Rand,
		log:            cfg.Logger,
		bearerFailures: newWindowLimiter(rateLimitWindow, bearerFailureLimit),
		connRequests:   newWindowLimiter(rateLimitWindow, connRequestLimit),
	}
	if a.rnd == nil {
		a.rnd = cryptoRand
	}
	if a.log == nil {
		a.log = slog.Default()
	}
	return a, nil
}

func (a *authz) Authorize(ctx context.Context, r Request) (Principal, error) {
	id, err := a.Identify(ctx, r.RemoteAddr)
	if err != nil {
		return Principal{}, err
	}
	client, err := a.CheckAllowlist(ctx, r.RemoteAddr, id, r.Feature)
	if err != nil {
		return Principal{}, err
	}
	p := Principal{Client: client, Identity: id}
	if r.Feature == FeatureMCP {
		if p.Token, err = a.VerifyBearer(ctx, r.RemoteAddr, p, r.Authorization); err != nil {
			return Principal{}, err
		}
	}
	return p, nil
}

func (a *authz) Identify(ctx context.Context, remoteAddr string) (Identity, error) {
	return ResolveIdentity(ctx, a.cfg.WhoIs, remoteAddr)
}

// ResolveIdentity は remoteAddr を WhoIs で解決する（連鎖の 4。NFR-S05）。
// 失敗、または node ID・ログイン名が欠けた不完全な結果は whois_failed（fail closed）。
func ResolveIdentity(ctx context.Context, whois tailnet.WhoIs, remoteAddr string) (Identity, error) {
	id, err := whois.Lookup(ctx, remoteAddr)
	if err != nil {
		if errors.Is(err, tailnet.ErrWhoIsFailed) {
			return Identity{}, err
		}
		return Identity{}, fmt.Errorf("%w: %w", tailnet.ErrWhoIsFailed, err)
	}
	if id.StableNodeID == "" || id.LoginName == "" {
		return Identity{}, fmt.Errorf("%w: incomplete identity", tailnet.ErrWhoIsFailed)
	}
	return id, nil
}

func (a *authz) CheckAllowlist(ctx context.Context, remoteAddr string, id Identity, f Feature) (domain.Client, error) {
	client, err := a.cfg.Clients.ByNode(ctx, id.StableNodeID)
	switch {
	case errors.Is(err, repository.ErrNotFound):
		return domain.Client{}, a.notRegistered(ctx, remoteAddr, id)
	case err != nil:
		return domain.Client{}, err
	case client.LoginName != id.LoginName:
		// 登録時と異なるユーザーで再認証されたデバイスは、未登録と同じく接続要求にする（FR-A06）。
		return domain.Client{}, a.notRegistered(ctx, remoteAddr, id)
	}
	enabled := client.WebEnabled
	if f == FeatureMCP {
		enabled = client.MCPEnabled
	}
	if !enabled {
		return domain.Client{}, ErrClientDisabled
	}
	return client, nil
}

// notRegistered は接続要求を作り（失敗してもリクエストは拒否のまま）、device_not_registered を返す。
func (a *authz) notRegistered(ctx context.Context, remoteAddr string, id Identity) error {
	if err := a.RecordConnectionRequest(ctx, id, remoteIP(remoteAddr)); err != nil {
		a.log.Warn("record connection request failed", "error", err)
	}
	return ErrDeviceNotRegistered
}

// remoteIP は "ip" または "ip:port" の IP 部分を返す。解釈できなければ入力をそのまま返す。
func remoteIP(remoteAddr string) string {
	if ap, err := netip.ParseAddrPort(remoteAddr); err == nil {
		return ap.Addr().WithZone("").Unmap().String()
	}
	if ip, err := netip.ParseAddr(remoteAddr); err == nil {
		return ip.WithZone("").Unmap().String()
	}
	return remoteAddr
}
