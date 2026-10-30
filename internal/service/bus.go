package service

import (
	"context"
	"sync"
	"time"

	"github.com/ousiassllc/puppet-remo/internal/repository"
)

// Bus のイベントの種類（docs/components/overview.md §5.10、API 仕様 §7.4）。M1 で使うものだけ。
const (
	TopicLease              = "lease"
	TopicConnectionRequests = "connection-requests"
	TopicKillSwitch         = "killswitch"
	TopicServerStatus       = "server-status"
	TopicSessions           = "sessions"
	TopicToast              = "toast"
)

// Event は Bus を流れる通知 1 件。Data は種類ごとの付随情報（toast の Toast など）で、無いこともある。
// 購読者は状態そのものを Event から読まず、現在の状態を読み直して描画する。
type Event struct {
	Topic string
	Data  any
}

// Toast は toast イベントの Data（ホスト GUI への通知）。
type Toast struct {
	Level   string // "info" | "error"
	Message string
}

// busBuffer は購読者ごとのバッファ。満杯のときは最も古いイベントを捨てる（最新は必ず届く）。
const busBuffer = 16

// Bus は GUI の SSE などへ状態の変化を知らせる購読型の通知（docs/components/overview.md §5.10）。
// Publish は購読者を待たせない。
type Bus struct {
	mu   sync.RWMutex
	subs map[*busSub]struct{}
}

type busSub struct {
	topics map[string]struct{} // 空なら全イベント
	mu     sync.Mutex          // 送信側同士の「捨てて送る」を直列化する
	ch     chan Event
}

// NewBus は購読者のいない Bus を返す。
func NewBus() *Bus { return &Bus{subs: map[*busSub]struct{}{}} }

// Publish は e を、その topic を購読している全員へ非ブロッキングで送る。
func (b *Bus) Publish(e Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for s := range b.subs {
		if len(s.topics) > 0 {
			if _, ok := s.topics[e.Topic]; !ok {
				continue
			}
		}
		s.send(e)
	}
}

func (s *busSub) send(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case s.ch <- e:
		return
	default:
	}
	// 満杯: 最も古いものを捨てて最新を入れる。
	select {
	case <-s.ch:
	default:
	}
	select {
	case s.ch <- e:
	default:
	}
}

// Subscribe は topics のイベントを受け取るチャネルと、購読を解除する関数を返す。topics が空なら全イベント。
// 解除後はチャネルへ送られず、チャネルは閉じない（受け手は解除と同時に読むのをやめる）。cancel は複数回呼んでよい。
func (b *Bus) Subscribe(topics ...string) (<-chan Event, func()) {
	s := &busSub{topics: map[string]struct{}{}, ch: make(chan Event, busBuffer)}
	for _, t := range topics {
		s.topics[t] = struct{}{}
	}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	return s.ch, func() {
		b.mu.Lock()
		delete(b.subs, s)
		b.mu.Unlock()
	}
}

// BridgeLeaseEvents は LeaseManager の変化を Bus の lease イベントへ流す。購読は呼び出しの中で済ませる
// （戻った後のリースの変化は必ず通知される）。goroutine は ctx の終了で止まり、止まったら返したチャネルが閉じる。
func BridgeLeaseEvents(ctx context.Context, leases LeaseManager, bus *Bus) <-chan struct{} {
	events := leases.Subscribe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-events:
				if !ok {
					return
				}
				bus.Publish(Event{Topic: TopicLease})
			}
		}
	}()
	return done
}

// HostKillSwitch は GUI が使うキルスイッチの窓口（発動・解除と、発動状態の詳細）。KillSwitchManager が満たす。
type HostKillSwitch interface {
	KillSwitch
	State() KillSwitchSnapshot
}

// notifyingKillSwitch は発動・解除のたびに Bus へ通知する（ホットキー・GUI・MCP のどこから呼ばれても同じ）。
type notifyingKillSwitch struct {
	HostKillSwitch
	bus *Bus
}

// NewNotifyingKillSwitch は inner の Trigger / Release の後に killswitch・lease・sessions を Bus へ通知する。
func NewNotifyingKillSwitch(inner HostKillSwitch, bus *Bus) HostKillSwitch {
	return &notifyingKillSwitch{HostKillSwitch: inner, bus: bus}
}

func (k *notifyingKillSwitch) Trigger(source string) error {
	err := k.HostKillSwitch.Trigger(source)
	k.bus.Publish(Event{Topic: TopicKillSwitch})
	k.bus.Publish(Event{Topic: TopicLease})
	k.bus.Publish(Event{Topic: TopicSessions})
	return err
}

func (k *notifyingKillSwitch) Release() error {
	err := k.HostKillSwitch.Release()
	k.bus.Publish(Event{Topic: TopicKillSwitch})
	return err
}

// notifyingConnRequests は接続要求の変化を Bus へ通知する repository.ConnRequests のラッパー。
type notifyingConnRequests struct {
	repository.ConnRequests
	bus *Bus
}

// NewNotifyingConnRequests は、新しい接続要求の作成・許可・拒否・失効の後に connection-requests を Bus へ通知する。
// 既存の pending への集約（再試行）では通知しない。Authz と HostAdmin の両方へこれを渡す。
func NewNotifyingConnRequests(inner repository.ConnRequests, bus *Bus) repository.ConnRequests {
	return &notifyingConnRequests{ConnRequests: inner, bus: bus}
}

func (n *notifyingConnRequests) notify() { n.bus.Publish(Event{Topic: TopicConnectionRequests}) }

func (n *notifyingConnRequests) UpsertPending(ctx context.Context, r repository.ConnRequest) (repository.ConnRequest, bool, error) {
	req, created, err := n.ConnRequests.UpsertPending(ctx, r)
	if err == nil && created {
		n.notify()
	}
	return req, created, err
}

func (n *notifyingConnRequests) Decide(ctx context.Context, id string, status string, clientID *string) error {
	err := n.ConnRequests.Decide(ctx, id, status, clientID)
	if err == nil {
		n.notify()
	}
	return err
}

func (n *notifyingConnRequests) Expire(ctx context.Context, now time.Time) error {
	err := n.ConnRequests.Expire(ctx, now)
	if err == nil {
		n.notify()
	}
	return err
}
