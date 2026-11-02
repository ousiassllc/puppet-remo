package gui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/gin-gonic/gin"

	"github.com/ousiassllc/puppet-remo/internal/service"
	"github.com/ousiassllc/puppet-remo/internal/web/atoms"
	"github.com/ousiassllc/puppet-remo/internal/web/organisms"
	"github.com/ousiassllc/puppet-remo/internal/web/view"
)

// sseTopics は GET /events が流すイベント（API 仕様 §7.4）。名前は Bus のトピックと SSE の event 名で共通。
// approval・tasks・approval-closed は SSE に流さない: 承認は専用ウィンドウ（/approvals/{id}）、タスクは /poll の rev_tasks で更新する。
var sseTopics = []string{
	service.TopicServerStatus, service.TopicSessions, service.TopicLease,
	service.TopicConnectionRequests, service.TopicKillSwitch, service.TopicToast,
}

// Events は GET /events（SSE）。data は描画済みの HTML フラグメント。
//
// Wails v3.0.0 の Windows の AssetServer は応答をバッファするため、Wails のウィンドウでは届かない。ホスト GUI のレイアウトは
// このストリームを購読せず、GET /poll（poll.go）で更新する。ストリームを流せる経路で GUI を配信するとき・テストのために残している。
//
//	server-status         → #server-status の中身
//	sessions              → #sessions の中身
//	lease                 → #lease の中身
//	connection-requests   → サイドバーの件数バッジ
//	killswitch            → ヘッダーの状態バッジ
//	toast                 → #toast-root に追加するトースト
//
// 接続が切れても HTMX の SSE 拡張が再接続するため、接続直後に各イベントの現在値を 1 回送る（toast を除く）。
func (h *Handler) Events(c *gin.Context) {
	ctx := c.Request.Context()
	events, cancel := h.d.Events.Subscribe(sseTopics...)
	defer cancel()

	hd := c.Writer.Header()
	hd.Set("Content-Type", "text/event-stream")
	hd.Set("Cache-Control", "no-cache")
	hd.Set("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	send := func(topic string, data []byte) bool {
		if err := writeSSE(c.Writer, topic, data); err != nil {
			return false
		}
		c.Writer.Flush()
		return true
	}
	for _, topic := range sseTopics {
		if topic == service.TopicToast {
			continue
		}
		if !h.sendTopic(ctx, send, topic, nil) {
			return
		}
	}

	tick := time.NewTicker(h.hb)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if _, err := io.WriteString(c.Writer, ": keepalive\n\n"); err != nil {
				return
			}
			c.Writer.Flush()
		case e, ok := <-events:
			if !ok || !h.sendTopic(ctx, send, e.Topic, e.Data) {
				return
			}
		}
	}
}

// sendTopic は topic の現在の状態を描画して送る。描画に失敗したイベントは飛ばす（接続は保つ）。
// 書き込みに失敗したら false（接続が切れている）。
func (h *Handler) sendTopic(ctx context.Context, send func(string, []byte) bool, topic string, data any) bool {
	comp := h.topicComponent(ctx, topic, data)
	if comp == nil {
		return true
	}
	var buf bytes.Buffer
	if err := comp.Render(ctx, &buf); err != nil {
		h.log.Error("render sse event failed", "topic", topic, "error", err)
		return true
	}
	return send(topic, buf.Bytes())
}

func (h *Handler) topicComponent(ctx context.Context, topic string, data any) templ.Component {
	switch topic {
	case service.TopicServerStatus:
		return organisms.ServerStatusBody(h.serverStatusView(ctx))
	case service.TopicSessions:
		return organisms.SessionListBody(h.sessionsView())
	case service.TopicLease:
		return organisms.LeaseDisplay(h.leaseView())
	case service.TopicConnectionRequests:
		return organisms.RequestsBadge(h.header(ctx).PendingRequests)
	case service.TopicKillSwitch:
		return organisms.HeaderKillSwitch(h.d.KillSwitch.Active())
	case service.TopicToast:
		t, ok := toastView(data)
		if !ok {
			return nil
		}
		return atoms.Toast(t)
	}
	return nil
}

// toastView は toast イベントの Data（service.Toast）を表示用のトーストにする。SSE と GET /poll で共通。
func toastView(data any) (view.Toast, bool) {
	t, ok := data.(service.Toast)
	if !ok {
		return view.Toast{}, false
	}
	level := view.ToastInfo
	if t.Level == "error" {
		level = view.ToastError
	}
	return view.Toast{Level: level, Message: t.Message}, true
}

// writeSSE は 1 イベントを書く。HTML の改行は data: 行に分ける（受信側が改行で連結し直す）。
func writeSSE(w io.Writer, event string, data []byte) error {
	var b strings.Builder
	fmt.Fprintf(&b, "event: %s\n", event)
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		b.WriteString("data: ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	_, err := io.WriteString(w, b.String())
	return err
}
