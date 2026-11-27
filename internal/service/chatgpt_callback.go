package service

import (
	"context"
	"crypto/subtle"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// このファイルは ChatGPT のサインインのコールバック受信口（API 仕様 §7.6・NFR-S21 (2)）: サインインの間だけ 127.0.0.1 に開く
// 1 つの小さな HTTP サーバー。リモートルーター・GUI ルーターとは別の http.Server で、受信口自身は資格情報を持たない。
// 受け取るのは state が一致する最初の 1 回の GET /auth/callback だけで、値（code・state など）は応答にも運用ログにも出さない（NFR-S13）。

const chatgptCallbackPath = "/auth/callback"

// 応答の静的な文言（コード・state・アカウント名を含めない。外部のリソースを読み込まない）。
const (
	chatgptCallbackDonePage = `<!doctype html><meta charset="utf-8"><title>puppet-remo</title>` +
		`<p>サインインの結果をアプリに渡しました。このタブを閉じてください。</p>`
	chatgptCallbackBadPage = `<!doctype html><meta charset="utf-8"><title>puppet-remo</title>` +
		`<p>この要求は受け付けられません。</p>`
)

// chatgptCallbackResult は state が一致した 1 回のコールバック。ErrCode が空でない、または PolicyDenied なら、認可の拒否・失敗。
type chatgptCallbackResult struct {
	Code         string
	ClientID     string // 新規登録のときに発行された client_id
	Scope        string // コールバックが報告する scope（確定はトークンの応答）
	ErrCode      string // error（access_denied・3p_delegated_access_policy_denied など）。error_description の文は保持しない
	PolicyDenied bool   // error か error_description がワークスペースのポリシー拒否の識別子を含む（FR-G40）
}

type chatgptCallback struct {
	srv      *http.Server
	state    string
	hostPort string // 受け付ける Host（127.0.0.1:<port>）
	port     int

	once      sync.Once
	result    chan chatgptCallbackResult
	closeOnce sync.Once
}

// newChatGPTCallback は ln（127.0.0.1 の受信口）で受け付けを始める。ln は close が閉じる。
func newChatGPTCallback(ln net.Listener, state string) *chatgptCallback {
	port := ln.Addr().(*net.TCPAddr).Port
	c := &chatgptCallback{
		state:    state,
		port:     port,
		hostPort: net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		result:   make(chan chatgptCallbackResult, 1),
	}
	c.srv = &http.Server{Handler: c, ReadHeaderTimeout: 10 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() { _ = c.srv.Serve(ln) }()
	return c
}

// wait は state が一致した最初のコールバックを待つ。ctx が終わったら ctx.Err()。
func (c *chatgptCallback) wait(ctx context.Context) (chatgptCallbackResult, error) {
	select {
	case r := <-c.result:
		return r, nil
	case <-ctx.Done():
		return chatgptCallbackResult{}, ctx.Err()
	}
}

// close は受信口を閉じる（冪等）。処理中の応答は書き終えさせる（短い上限つき）。
func (c *chatgptCallback) close() {
	c.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := c.srv.Shutdown(ctx); err != nil {
			_ = c.srv.Close()
		}
	})
}

func writeChatGPTCallbackPage(w http.ResponseWriter, status int, body string) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", "default-src 'none'")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func (c *chatgptCallback) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Host != c.hostPort: // DNS リバインディング対策（localhost・他のホスト名を拒む）
		writeChatGPTCallbackPage(w, http.StatusBadRequest, chatgptCallbackBadPage)
		return
	case r.URL.Path != chatgptCallbackPath:
		writeChatGPTCallbackPage(w, http.StatusNotFound, chatgptCallbackBadPage)
		return
	case r.Method != http.MethodGet:
		w.Header().Set("Allow", http.MethodGet)
		writeChatGPTCallbackPage(w, http.StatusMethodNotAllowed, chatgptCallbackBadPage)
		return
	}
	q := r.URL.Query()
	if st := q.Get("state"); st == "" || subtle.ConstantTimeCompare([]byte(st), []byte(c.state)) != 1 {
		writeChatGPTCallbackPage(w, http.StatusBadRequest, chatgptCallbackBadPage) // 待ちを続ける
		return
	}
	res := chatgptCallbackResult{ErrCode: q.Get("error")}
	// ポリシー拒否の識別子が error と error_description のどちらに来るかは未確認（FR-G40 (1)）。両方を見て、説明文そのものは保持しない。
	res.PolicyDenied = chatgptMentionsPolicyDenied(res.ErrCode) || chatgptMentionsPolicyDenied(q.Get("error_description"))
	if res.ErrCode == "" && !res.PolicyDenied {
		res.Code, res.ClientID, res.Scope = q.Get("code"), q.Get("client_id"), q.Get("scope")
		if res.Code == "" {
			writeChatGPTCallbackPage(w, http.StatusBadRequest, chatgptCallbackBadPage)
			return
		}
	}
	delivered := false
	c.once.Do(func() {
		c.result <- res
		delivered = true
	})
	if !delivered { // 最初の 1 回だけを受け取る
		writeChatGPTCallbackPage(w, http.StatusBadRequest, chatgptCallbackBadPage)
		return
	}
	writeChatGPTCallbackPage(w, http.StatusOK, chatgptCallbackDonePage)
}
