package mcp

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ousiassllc/puppet-remo/internal/domain"
	"github.com/ousiassllc/puppet-remo/internal/service"
)

// fakeApproval はホストの結果を、テストが answer に流すまで待つ service.Approval の偽物。
type fakeApproval struct {
	requested chan service.ApprovalRequest // Request が来るたびに 1 件流す
	answer    chan approvalAnswer
}

type approvalAnswer struct {
	allow bool
	err   error
}

func newFakeApproval() *fakeApproval {
	return &fakeApproval{requested: make(chan service.ApprovalRequest, 8), answer: make(chan approvalAnswer, 1)}
}

func (a *fakeApproval) Request(ctx context.Context, req service.ApprovalRequest) (service.Decision, error) {
	a.requested <- req
	select {
	case r := <-a.answer:
		return service.Decision{Allow: r.allow}, r.err
	case <-ctx.Done():
		return service.Decision{}, ctx.Err()
	}
}

func (*fakeApproval) Resolve(string, bool) error         { return nil }
func (*fakeApproval) Pending() []service.ApprovalRequest { return nil }

// fakeLeaseClients はどのクライアントも承認モード always の service.LeaseClients。
type fakeLeaseClients struct{}

func (*fakeLeaseClients) Get(_ context.Context, id string) (domain.Client, error) {
	return domain.Client{ID: id, ApprovalMode: domain.ApprovalAlways}, nil
}

func (*fakeLeaseClients) MarkApproved(context.Context, string, time.Time) error { return nil }

// newApprovalEnv は承認が要る（always）クライアントの LeaseManager を持つ環境。
func newApprovalEnv(t *testing.T) (*env, *fakeApproval) {
	t.Helper()
	appr := newFakeApproval()
	e := newEnvWithLeases(t, func(c *service.LeaseConfig) {
		c.Approval = appr
		c.Clients = &fakeLeaseClients{}
	})
	return e, appr
}

// callAsync はツールを別 goroutine で呼ぶ（承認で待たされるため）。結果は返したチャネルに 1 回届く。
func callAsync(t *testing.T, cs *mcpsdk.ClientSession, name string, args any) <-chan result {
	t.Helper()
	out := make(chan result, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
		defer cancel()
		res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Errorf("call %s: %v", name, err)
			out <- result{&mcpsdk.CallToolResult{IsError: true}}
			return
		}
		out <- result{res}
	}()
	return out
}

func awaitRequest(t *testing.T, a *fakeApproval) service.ApprovalRequest {
	t.Helper()
	select {
	case r := <-a.requested:
		return r
	case <-time.After(waitLimit):
		t.Fatal("no approval request was made")
		return service.ApprovalRequest{}
	}
}

func awaitResult(t *testing.T, ch <-chan result) result {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(waitLimit):
		t.Fatal("the tool call did not return")
		return result{}
	}
}

// 承認を待っている間の get_control_status は pending_approval（holder なし、mine は要求者だけ）。許可されたら held になり、
// acquire_control が held の状態を返す（実 LeaseManager + 偽の Approval の結合）。
func TestAcquireControlWaitsForTheHostApproval(t *testing.T) {
	e, appr := newApprovalEnv(t)
	requester, other := e.connect("a"), e.connect("b")

	done := callAsync(t, requester, "acquire_control", nil)
	req := awaitRequest(t, appr)
	if req.Kind != service.ApprovalKindLease || req.Who.Kind != domain.ActorMCP || req.Who.ClientID != "a" {
		t.Errorf("approval request = %+v, want a lease request by the mcp client a", req)
	}

	if s := status(t, other); s.State != "pending_approval" || s.Holder != nil || s.Mine || s.IdleRemainingSec != nil {
		t.Errorf("status seen by another session = %+v, want pending_approval without holder, mine false", s)
	}
	if s := status(t, requester); s.State != "pending_approval" || s.Holder != nil || !s.Mine {
		t.Errorf("status seen by the requester = %+v, want pending_approval with mine true", s)
	}
	// 承認待ちの間、他者の取得は lease_held_by_other。
	if res := callTool(t, other, "acquire_control", nil); res.errCode(t) != domain.CodeLeaseHeldByOther {
		t.Errorf("acquire during pending_approval: %s", res.text())
	}

	appr.answer <- approvalAnswer{allow: true}
	res := awaitResult(t, done)
	if res.IsError {
		t.Fatalf("acquire_control: %s", res.text())
	}
	var got statusBody
	res.decode(t, &got)
	if got.State != "held" || !got.Mine || got.Holder == nil || got.Holder.Kind != "mcp" {
		t.Errorf("acquire_control = %+v, want held by the requester", got)
	}
	if s := status(t, other); s.State != "held" || s.Mine {
		t.Errorf("status after approval = %+v, want held by someone else", s)
	}
}

// 拒否は approval_denied（403）、タイムアウトは approval_timeout（408）。どちらも idle に戻る。
// 暗黙取得（入力系ツール）でも同じ結果になる。
func TestApprovalFailuresAreToolErrors(t *testing.T) {
	tests := []struct {
		name     string
		answer   approvalAnswer
		wantCode domain.Code
		wantHTTP int
	}{
		{"denied", approvalAnswer{allow: false}, domain.CodeApprovalDenied, http.StatusForbidden},
		{"timeout", approvalAnswer{err: domain.ErrApprovalTimeout}, domain.CodeApprovalTimeout, http.StatusRequestTimeout},
	}
	for _, tt := range tests {
		for _, tool := range []string{"acquire_control", "left_click"} {
			t.Run(tt.name+"/"+tool, func(t *testing.T) {
				e, appr := newApprovalEnv(t)
				cs := e.connect("a")

				done := callAsync(t, cs, tool, nil)
				awaitRequest(t, appr)
				appr.answer <- tt.answer

				res := awaitResult(t, done)
				if got := res.errCode(t); got != tt.wantCode {
					t.Fatalf("code = %s, want %s (%s)", got, tt.wantCode, res.text())
				}
				if got := tt.wantCode.HTTPStatus(); got != tt.wantHTTP {
					t.Errorf("HTTP status of %s = %d, want %d", tt.wantCode, got, tt.wantHTTP)
				}
				var body errorBody
				res.decode(t, &body)
				if body.Message == "" || body.Detail != nil || body.Holder != nil {
					t.Errorf("error body = %+v, want only a message", body)
				}
				if s := status(t, cs); s.State != "idle" || s.Mine {
					t.Errorf("status after %s = %+v, want idle", tt.name, s)
				}
			})
		}
	}
}

// 承認待ちの Approval.Request がエラーを返す（取り下げ）ときも、想定外の internal にならず idle に戻る。
func TestApprovalRequestErrorFallsBackToIdle(t *testing.T) {
	e, appr := newApprovalEnv(t)
	cs := e.connect("a")

	done := callAsync(t, cs, "acquire_control", nil)
	awaitRequest(t, appr)
	appr.answer <- approvalAnswer{err: errors.New("approval backend failed")}

	res := awaitResult(t, done)
	if !res.IsError {
		t.Fatalf("acquire_control succeeded: %s", res.text())
	}
	if s := status(t, cs); s.State != "idle" {
		t.Errorf("status = %+v, want idle", s)
	}
}
