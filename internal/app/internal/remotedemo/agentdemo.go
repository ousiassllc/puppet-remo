package main

import (
	"cmp"
	"context"
	"encoding/json"
	"log"
	"net"
	"sync"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/repository"
	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/service/agenttest"
)

// このファイルは -agent-demo（オフラインで内蔵エージェントのタスクを最後まで動かす）の部品。OpenAI も TypeSafe も呼ばない:
// 台本つきの偽の openai.Model を app.Config.OpenAIModel に差し込み、キーは「登録されている」ことを示すだけの明らかなダミーを
// 偽の SecretStore に入れる（値は何にも使わない）。

const (
	// demoToken はデモのクライアントの Bearer トークン（NFR-S04 の形: prm_ + 43 文字）。明らかなダミーで、デモのプロセスの外では何も開けない。
	demoToken = "prm_DEMO-dummy-token-not-a-secret-0123456789xxA"
	// demoStepDelay は台本の 1 ステップの所要時間（モデルの応答の待ちの代わり）。オーバーレイ・ダッシュボードのスクリーンショットを撮れるよう長めにする。
	demoStepDelay = 1500 * time.Millisecond
)

// registerAgentDemo はエージェントを使える状態にする: 偽の SecretStore に OpenAI・TypeSafe のダミーのキー（存在の目印だけ）を入れ、
// 設定でエージェントを有効にして同意を記録し（実際の GUI の「有効化」と同じ設定キー・同意の日時）、デモのクライアントの Bearer トークンを作る。
func registerAgentDemo(ctx context.Context, db *repository.DB, secrets platform.SecretStore, clientID string) error {
	for _, name := range []string{platform.SecretOpenAI, platform.SecretTypeSafe} {
		if err := secrets.Set(name, "demo-dummy-key-presence-marker-only"); err != nil {
			return err
		}
	}
	consent := time.Now().UnixMilli()
	for key, val := range map[string]any{service.SettingAgentEnabled: true, service.SettingAgentConsentAt: consent} {
		raw, err := json.Marshal(val)
		if err != nil {
			return err
		}
		if err := db.Settings().Set(ctx, key, raw); err != nil {
			return err
		}
	}
	return db.Tokens().Create(ctx, repository.TokenRecord{
		ID: "token-demo-1", ClientID: clientID, Label: "agent-demo", TokenHash: repository.HashToken(demoToken),
		Prefix: repository.TokenDisplayPrefix(demoToken), CreatedAt: time.Now(),
	})
}

// demoModel は台本どおりに答える偽の openai.Model: クリック → 文字入力 → 最終回答（safetyCheck なら 2 ステップ目に安全チェックが付く）。
// 各ステップは demoStepDelay だけ待つ（ctx で打ち切れる。中断・キルスイッチも本物と同じ経路で効く）。台本の進み具合は、タスクごとに
// 新しくなる履歴（*agenttest.History）の同一性で数え直す（デモは同時に 1 つのタスクしか動かさない）。
type demoModel struct {
	safetyCheck bool
	delay       time.Duration // 0 は demoStepDelay（テストだけが短くする）

	mu   sync.Mutex
	hist *agenttest.History
	n    int
}

func (m *demoModel) step(h *agenttest.History) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if h != m.hist {
		m.hist, m.n = h, 0
	}
	m.n++
	return m.n
}

func (m *demoModel) Step(ctx context.Context, in agenttest.StepInput) (agenttest.StepOutput, error) {
	select {
	case <-time.After(cmp.Or(m.delay, demoStepDelay)):
	case <-ctx.Done():
		return agenttest.StepOutput{}, ctx.Err()
	}
	call := agenttest.Call
	switch m.step(in.History) {
	case 1:
		// メモ帳の本文（偽のデスクトップのメモ帳の中）をクリックする。座標は縮小座標（デモのモニターは縮小しない）。
		return call("call_demo_1", domain.Click{At: &domain.Point{X: 420, Y: 320}, Button: domain.ButtonLeft, Count: 1, Monitor: domain.MonitorPrimary}), nil
	case 2:
		out := call("call_demo_2", domain.TypeText{Text: "Hello from the demo agent"})
		if m.safetyCheck {
			out.SafetyChecks = []agenttest.SafetyCheck{{
				ID: "sc_demo_1", Code: "malicious_instructions",
				Message: "The page you are viewing appears to contain instructions that differ from your request. (demo)",
			}}
		}
		return out, nil
	}
	return agenttest.Final("Typed a greeting into Notes. (demo)"), nil
}

// logAgentDemo はデモの使い方をログに出す。
func logAgentDemo(remoteAddr string) {
	log.Printf("remotedemo: agent demo is on. The demo client may run tasks; the OpenAI model is a script (nothing leaves this machine).")
	log.Printf("remotedemo: start a task (the Host header must be the fake tailnet IP):")
	log.Printf(`  curl -s -H "Host: %s:%s" -H "Authorization: Bearer %s" -H "Content-Type: application/json" -H "Accept: application/json, text/event-stream" \`, demoTailnet, demoPort, demoToken)
	log.Printf(`    -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"curl","version":"0"}}}' http://%s/mcp -D -`, remoteAddr)
	log.Printf("  (then send the Mcp-Session-Id header from that response with tools/call run_task / get_task / cancel_task; see the remotedemo doc comment)")
}

// tailnetFacade は loopback の Listener が返す接続の LocalAddr を、偽の tailnet アドレス（Host ヘッダーと同じ）に見せる。
// MCP の SDK は、loopback 宛ての接続に loopback 以外の Host が付くと 403 にする（DNS リバインディング対策）。
// 本番は tailnet の IP で待ち受けるのでこの検査に当たらないが、デモは loopback なので、curl に偽の Host を付けるには要る。
type tailnetFacade struct {
	net.Listener
	local string
}

func (f tailnetFacade) Accept() (net.Conn, error) {
	c, err := f.Listener.Accept()
	if err != nil {
		return nil, err
	}
	addr, err := net.ResolveTCPAddr("tcp", f.local)
	if err != nil {
		return c, nil
	}
	return facadeConn{Conn: c, local: addr}, nil
}

type facadeConn struct {
	net.Conn
	local net.Addr
}

func (c facadeConn) LocalAddr() net.Addr { return c.local }
