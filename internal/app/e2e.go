//go:build e2e

package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform/platformtest"
	"github.com/ousiassllc/puppet-remo/internal/repository"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/tailnet"
)

// このファイルは E2E（Playwright。docs/architecture/frontend.md §9）のテスト専用サーバーの組み立てと、テスト用の制御エンドポイント。
// ビルドタグ e2e を付けたときだけコンパイルされ、本番のバイナリ（cmd/puppet-remo）には入らない。
//
// 本物の service・ルーター・WebSocket を app.New で組み立て、OS 依存部（画面・入力・クリップボードなど）は platformtest の偽物、
// tailnet は tailnet の e2e タグのファイルにある偽物（Watcher・WhoIs）と、loopback に Listen する ListenFunc にする。
// リモートルーターは loopback（127.0.0.1）で待ち受ける。NFR-S01（tailnet の IP だけで Listen する）の例外はこのビルドタグに限る。
//
// 制御エンドポイント（ホスト側の操作と、偽の injector に届いた入力の読み取り）は、リモートルーターとは別のソケットで、
// loopback の IP リテラル（127.0.0.1・::1）にしか bind しない（e2eLoopbackAddr が検査する）。認証は無い。

// E2E の偽のデバイス（架空の値。実在のアカウント・ホスト名ではない）。
const (
	e2eRegisteredClientID = "client-e2e-1"
	e2eRegisteredNode     = "node-e2e-1"
)

var (
	e2eRegisteredIdentity   = tailnet.Identity{LoginName: "e2e-user@example.com", StableNodeID: e2eRegisteredNode, NodeName: "e2e-laptop"}
	e2eUnregisteredIdentity = tailnet.Identity{LoginName: "guest@example.com", StableNodeID: "node-e2e-9", NodeName: "e2e-phone"}
)

// E2EAddrs は RunE2E が待ち受けているアドレス。
type E2EAddrs struct {
	Remote  string // リモートルーター（loopback。ポートは設定 server.port と同じ）
	Control string // 制御エンドポイント
}

// E2EOptions は RunE2E の設定。
type E2EOptions struct {
	// Port はリモートルーターのポート（設定 server.port。Host 検査は報告された IP・名前とこのポートだけを通す）。1024〜65535。
	Port int
	// ControlAddr は制御エンドポイントの待ち受けアドレス（loopback の IP リテラル:ポート。ポート 0 なら空きポート）。
	ControlAddr string
	// Static は /static/* を配信するハンドラ（static.Handler）。
	Static http.Handler
	// Logger は nil なら標準エラーへのテキストログ（Info）。
	Logger *slog.Logger
	// Ready は制御エンドポイントが待ち受けを始めた直後に呼ぶ（nil なら呼ばない）。リモートルーターの準備完了は GET /ready で確かめる。
	Ready func(E2EAddrs)
}

// e2eLoopbackAddr は addr（host:port）の host が loopback の IP リテラルであることを検査する。
// 名前（localhost など）・未指定（0.0.0.0、::、空）・loopback 以外の IP は拒否する（名前は loopback 以外へ解決されうるため）。
func e2eLoopbackAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if p, err := strconv.Atoi(port); err != nil || p < 0 || p > 65535 {
		return fmt.Errorf("e2e: invalid port in %q", addr)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("e2e: control endpoints listen on a loopback IP literal only (got %q)", addr)
	}
	return nil
}

// RunE2E はテスト専用サーバーを動かし、ctx の終了で止めて（DB の一時ディレクトリも消して）返る。
func RunE2E(ctx context.Context, opts E2EOptions) error {
	if opts.Port < 1024 || opts.Port > 65535 {
		return fmt.Errorf("e2e: port must be 1024-65535 (got %d)", opts.Port)
	}
	if opts.Static == nil {
		return errors.New("e2e: Static is required")
	}
	if err := e2eLoopbackAddr(opts.ControlAddr); err != nil {
		return err
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}

	dir, err := os.MkdirTemp("", "puppet-remo-e2e-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	db, err := repository.Open(ctx, filepath.Join(dir, "puppet-remo.db"))
	if err != nil {
		return err
	}
	if err := db.Settings().Set(ctx, "server.port", json.RawMessage(strconv.Itoa(opts.Port))); err != nil {
		_ = db.Close()
		return err
	}

	srv := &e2eServer{db: db, whois: tailnet.NewE2EWhoIs(e2eUnregisteredIdentity), input: platformtest.NewFakeInput()}
	secrets := platformtest.NewFakeSecretStore()
	clip := platformtest.NewFakeClipboard()
	// 偽のデスクトップ（e2e_desktop.go）。前面のウィンドウ（偽アプリ T）が無いと、入力は「ホストの画面を操作できません」で拒否される。
	srv.desktop = newE2EDesktop(srv.input, clip)
	hostWindows := srv.desktop.windows
	a, err := New(ctx, Config{
		Repo:    db,
		Windows: NewFakeWindows(),
		Platform: Platform{
			Capturer: srv.desktop.capturer, Input: srv.desktop.app, Windows: hostWindows, Clipboard: clip,
			UI: platformtest.NewFakeUIAutomation(hostWindows), Launcher: platformtest.NewFakeAppLauncher(),
			FilePicker: platformtest.NewFakeFilePicker(), Installer: platformtest.NewFakeInstaller(), Secrets: secrets,
			Hotkey: platformtest.NewFakeHotkey(), Power: platformtest.NewFakePower(),
		},
		Watcher: tailnet.NewE2EWatcher(tailnet.E2EStatus()), WhoIs: srv.whois,
		Static: opts.Static, Logs: NewLogReader(dir, WithFolderOpener(platformtest.NewFakeFolderOpener())),
		Version: "e2e", Logger: logger,
		// tailnet の IPv4 の代わりに、同じポートの 127.0.0.1 で待ち受ける（本物の Listener の絞り込み・State はそのまま）。
		ListenFunc: tailnet.LoopbackListenFunc(),
		Closers:    []io.Closer{db},
	})
	if err != nil {
		_ = db.Close()
		return err
	}
	srv.app = a
	if err := srv.seed(ctx); err != nil {
		_ = a.Shutdown(context.Background())
		return err
	}
	if err := a.Start(ctx); err != nil {
		_ = a.Shutdown(context.Background())
		return err
	}

	ln, err := net.Listen("tcp", opts.ControlAddr)
	if err != nil {
		_ = a.Shutdown(context.Background())
		return err
	}
	control := &http.Server{Handler: srv.handler(), ReadHeaderTimeout: 10 * time.Second}
	served := make(chan error, 1)
	go func() { served <- control.Serve(ln) }()

	logger.Info("e2e server started", "remote", net.JoinHostPort("127.0.0.1", strconv.Itoa(opts.Port)), "control", ln.Addr().String())
	if opts.Ready != nil {
		opts.Ready(E2EAddrs{Remote: net.JoinHostPort("127.0.0.1", strconv.Itoa(opts.Port)), Control: ln.Addr().String()})
	}

	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-served:
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = control.Shutdown(shutdown)
	if err := a.Shutdown(shutdown); err != nil && serveErr == nil {
		serveErr = err
	}
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	return serveErr
}

// e2eServer は制御エンドポイントが操作するもの。
type e2eServer struct {
	app   *App
	db    *repository.DB
	whois *tailnet.E2EWhoIs
	input *platformtest.FakeInput
	// desktop は偽のデスクトップ（偽アプリ・背景のウィンドウ。e2e_desktop.go）。
	desktop *e2eDesktop

	mu sync.Mutex // reset の直列化
}

// seed は「登録済みのデバイス」（identity?as=registered で名乗れるもの）を作る。
func (s *e2eServer) seed(ctx context.Context) error {
	return s.db.Clients().Create(ctx, domain.Client{
		ID: e2eRegisteredClientID, StableNodeID: e2eRegisteredNode, LoginName: e2eRegisteredIdentity.LoginName,
		DisplayName: e2eRegisteredIdentity.NodeName, WebEnabled: true, MCPEnabled: true, ApprovalMode: domain.ApprovalAuto,
	})
}

// handler は制御エンドポイント。loopback 以外からの接続は（bind が loopback だけでも）403 にする。
func (s *e2eServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ready", s.ready)
	mux.HandleFunc("POST /identity", s.identity)
	mux.HandleFunc("GET /requests", s.requests)
	mux.HandleFunc("POST /requests/allow", s.allow)
	mux.HandleFunc("GET /lease", s.lease)
	mux.HandleFunc("POST /lease/revoke", s.revoke)
	mux.HandleFunc("POST /killswitch", s.killSwitch)
	mux.HandleFunc("POST /killswitch/release", s.killSwitchRelease)
	mux.HandleFunc("GET /inputs", s.inputs)
	mux.HandleFunc("POST /reset", s.reset)
	s.desktopHandlers(mux)
	return loopbackOnly(mux)
}

func loopbackOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
			http.Error(w, "loopback only", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// ready はリモートルーターが待ち受けを始めたら 200（Playwright の webServer の待ち受け先）。
func (s *e2eServer) ready(w http.ResponseWriter, r *http.Request) {
	st := s.app.ServerStatus(r.Context())
	if !st.Listening {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ready": false, "reason": st.Reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ready": true, "listen_addrs": st.ListenAddrs})
}

// identity は以後の接続元のデバイスを切り替える（as=registered: 登録済みのデバイス、as=unregistered: 未登録のデバイス）。
func (s *e2eServer) identity(w http.ResponseWriter, r *http.Request) {
	switch as := r.URL.Query().Get("as"); as {
	case "registered":
		s.whois.Set(e2eRegisteredIdentity)
	case "unregistered":
		s.whois.Set(e2eUnregisteredIdentity)
	default:
		writeErr(w, http.StatusBadRequest, errors.New("as=registered|unregistered"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"as": r.URL.Query().Get("as")})
}

type e2eRequest struct {
	ID          string `json:"id"`
	NodeID      string `json:"node_id"`
	DisplayName string `json:"display_name"`
}

// requests は保留中の接続要求（ホストの接続要求の画面に出るもの）。
func (s *e2eServer) requests(w http.ResponseWriter, r *http.Request) {
	pending, err := s.app.admin.PendingRequests(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]e2eRequest, 0, len(pending))
	for _, p := range pending {
		out = append(out, e2eRequest{ID: p.ID, NodeID: p.StableNodeID, DisplayName: p.DisplayName})
	}
	writeJSON(w, http.StatusOK, out)
}

// allow は接続要求を許可する（ホストの GUI の「許可」と同じ HostAdmin.ApproveRequest）。id が無ければ、今名乗っているデバイスの保留中の要求。
// approval（auto・first_time・always）はリース取得時の承認モード。省略は auto（承認なしでリースを取る）。
func (s *e2eServer) allow(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		ident, err := s.whois.Lookup(r.Context(), "")
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		pending, err := s.app.admin.PendingRequests(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		for _, p := range pending {
			if p.StableNodeID == ident.StableNodeID {
				id = p.ID
				break
			}
		}
		if id == "" {
			writeErr(w, http.StatusNotFound, errors.New("no pending request for the current identity"))
			return
		}
	}
	mode := domain.ApprovalMode(r.URL.Query().Get("approval"))
	if mode == "" {
		mode = domain.ApprovalAuto
	}
	c, err := s.app.admin.ApproveRequest(r.Context(), id, service.ApproveInput{WebEnabled: true, MCPEnabled: true, ApprovalMode: mode})
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"client_id": c.ID})
}

// lease は今のリース保持者（いなければ held: false）。
func (s *e2eServer) lease(w http.ResponseWriter, _ *http.Request) {
	h, ok := s.app.leases.Holder()
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"held": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"held": true, "kind": string(h.Actor.Kind), "client_id": h.Actor.ClientID})
}

// revoke はホストによる強制剥奪（ホスト GUI の「強制剥奪」と同じ LeaseManager.Revoke(HostActor)）。
func (s *e2eServer) revoke(w http.ResponseWriter, _ *http.Request) {
	if err := s.app.leases.Revoke(service.HostActor); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"revoked": true})
}

// killSwitch はキルスイッチの発動（ホットキー・トレイと同じ経路）。
func (s *e2eServer) killSwitch(w http.ResponseWriter, _ *http.Request) {
	if err := s.app.kill.Trigger("e2e"); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"active": s.app.kill.Active()})
}

func (s *e2eServer) killSwitchRelease(w http.ResponseWriter, _ *http.Request) {
	if err := s.app.kill.Release(); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"active": s.app.kill.Active()})
}

type e2eInputCall struct {
	Method string `json:"method"`
	Args   []any  `json:"args"`
}

// inputs は偽の injector に届いた入力（OS に注入せず記録だけしたもの）を、届いた順に返す。
func (s *e2eServer) inputs(w http.ResponseWriter, _ *http.Request) {
	calls := s.input.Calls()
	out := make([]e2eInputCall, 0, len(calls))
	for _, c := range calls {
		args := make([]any, 0, len(c.Args))
		for _, a := range c.Args {
			args = append(args, e2eArg(a))
		}
		out = append(out, e2eInputCall{Method: c.Method, Args: args})
	}
	writeJSON(w, http.StatusOK, out)
}

func e2eArg(v any) any {
	switch v := v.(type) {
	case domain.Point:
		return map[string]int{"x": v.X, "y": v.Y}
	case int, float64, string, bool:
		return v
	default:
		return fmt.Sprint(v)
	}
}

// reset はテスト間の状態を初期に戻す: キルスイッチの解除 → リースの解放 → 登録済みのクライアントの削除（セッションも閉じる）→ 入力の記録の消去
// → 名乗るデバイスを未登録に戻す → 登録済みのデバイスの再作成。保留中の接続要求は消さない（同じデバイスの要求は 1 件に集約される）。
func (s *e2eServer) reset(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := r.Context()
	if s.app.kill.Active() {
		if err := s.app.kill.Release(); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	s.app.leases.ReleaseAll(string(domain.ReleaseExplicit))
	clients, err := s.app.admin.Clients(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	for _, c := range clients {
		if err := s.app.admin.DeleteClient(ctx, c.ID); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	s.input.Reset()
	s.desktop.reset()
	s.whois.Set(e2eUnregisteredIdentity)
	if err := s.seed(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"reset": true})
}
