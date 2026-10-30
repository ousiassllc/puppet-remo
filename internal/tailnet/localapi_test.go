package tailnet

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"tailscale.com/client/local"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
)

func TestStatusFrom(t *testing.T) {
	self := &ipnstate.PeerStatus{DNSName: "host.tail1234.ts.net."}
	tests := []struct {
		name string
		in   ipnstate.Status
		want Status
	}{
		{"running", ipnstate.Status{BackendState: "Running", TailscaleIPs: ips("100.64.0.1", "fd7a:115c:a1e0::1"), Self: self},
			Status{LoggedIn: true, IPs: ips("100.64.0.1", "fd7a:115c:a1e0::1"), DNSName: "host.tail1234.ts.net"}},
		{"starting has no IPs yet", ipnstate.Status{BackendState: "Starting", TailscaleIPs: ips("100.64.0.1"), Self: self},
			Status{LoggedIn: true, DNSName: "host.tail1234.ts.net"}},
		{"stopped", ipnstate.Status{BackendState: "Stopped", TailscaleIPs: ips("100.64.0.1")}, Status{LoggedIn: true}},
		{"needs login", ipnstate.Status{BackendState: "NeedsLogin"}, Status{}},
		{"needs machine auth", ipnstate.Status{BackendState: "NeedsMachineAuth"}, Status{}},
		{"no state", ipnstate.Status{BackendState: "NoState", TailscaleIPs: ips("100.64.0.1")}, Status{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := statusFrom(&tt.in)
			if !statusEqual(got, tt.want) {
				t.Errorf("statusFrom = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestIdentityFrom(t *testing.T) {
	node := &tailcfg.Node{StableID: "nABC123CNTRL", ComputedName: "laptop"}
	user := &tailcfg.UserProfile{LoginName: "alice@example.com"}
	tests := []struct {
		name string
		in   *apitype.WhoIsResponse
		want Identity
		ok   bool
	}{
		{"complete", &apitype.WhoIsResponse{Node: node, UserProfile: user},
			Identity{LoginName: "alice@example.com", StableNodeID: "nABC123CNTRL", NodeName: "laptop"}, true},
		{"nil", nil, Identity{}, false},
		{"no node", &apitype.WhoIsResponse{UserProfile: user}, Identity{}, false},
		{"no user", &apitype.WhoIsResponse{Node: node}, Identity{}, false},
		{"empty login", &apitype.WhoIsResponse{Node: node, UserProfile: &tailcfg.UserProfile{}}, Identity{}, false},
		{"empty node id", &apitype.WhoIsResponse{Node: &tailcfg.Node{}, UserProfile: user}, Identity{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := identityFrom(tt.in)
			if (err == nil) != tt.ok || got != tt.want {
				t.Errorf("identityFrom = %+v, %v; want %+v ok=%v", got, err, tt.want, tt.ok)
			}
		})
	}
}

// newLocalAPIAgainst は h を tailscaled の LocalAPI に見立てた LocalAPI を返す。
// tailscale.com/client/local の実クライアントを通すので、パスと JSON の形が合っていることを確かめられる。
func newLocalAPIAgainst(t *testing.T, h http.Handler) LocalAPI {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return NewLocalAPI(&local.Client{
		OmitAuth: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, srv.Listener.Addr().String())
		},
	})
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Error(err)
	}
}

func TestLocalAPIAgainstLocalClient(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/localapi/v0/status", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("peers") != "false" {
			t.Errorf("status must be requested without peers: %q", r.URL.RawQuery)
		}
		writeJSON(t, w, ipnstate.Status{
			BackendState: "Running",
			TailscaleIPs: ips("100.64.0.1", "fd7a:115c:a1e0::1"),
			Self:         &ipnstate.PeerStatus{DNSName: "host.tail1234.ts.net."},
		})
	})
	mux.HandleFunc("/localapi/v0/whois", func(w http.ResponseWriter, r *http.Request) {
		switch addr := r.URL.Query().Get("addr"); addr {
		case "100.64.0.5":
			writeJSON(t, w, apitype.WhoIsResponse{
				Node:        &tailcfg.Node{StableID: "nXYZ", ComputedName: "phone"},
				UserProfile: &tailcfg.UserProfile{LoginName: "bob@example.com"},
			})
		default:
			http.Error(w, "no match for IP:port", http.StatusNotFound)
		}
	})
	mux.HandleFunc("/localapi/v0/watch-ipn-bus", func(w http.ResponseWriter, r *http.Request) {
		running := ipn.Running
		enc := json.NewEncoder(w)
		_ = enc.Encode(ipn.Notify{PeersRemoved: []tailcfg.NodeID{1}}) // 無関係な通知は読み飛ばされる
		_ = enc.Encode(ipn.Notify{State: &running})
		w.(http.Flusher).Flush()
		// 切断（tailscaled の停止）まで待つ。
		<-r.Context().Done()
	})
	api := newLocalAPIAgainst(t, mux)
	ctx := context.Background()

	st, err := api.StatusWithoutPeers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !st.LoggedIn || !slices.Equal(st.IPs, ips("100.64.0.1", "fd7a:115c:a1e0::1")) || st.DNSName != "host.tail1234.ts.net" {
		t.Errorf("status = %+v", st)
	}

	id, err := api.WhoIs(ctx, "100.64.0.5")
	if err != nil {
		t.Fatal(err)
	}
	if id != (Identity{LoginName: "bob@example.com", StableNodeID: "nXYZ", NodeName: "phone"}) {
		t.Errorf("identity = %+v", id)
	}
	if _, err := api.WhoIs(ctx, "100.64.0.6"); err == nil {
		t.Error("an unknown peer must be an error")
	}

	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	bus, err := api.WatchIPNBus(wctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bus.Close() }()
	if err := bus.Next(); err != nil {
		t.Fatalf("Next = %v", err)
	}
	cancel()
	if err := bus.Next(); err == nil {
		t.Error("Next must fail once the subscription is canceled")
	}
}

func TestLocalAPIUnreachable(t *testing.T) {
	api := NewLocalAPI(&local.Client{
		OmitAuth: true,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			return nil, &net.OpError{Op: "dial", Err: net.ErrClosed}
		},
	})
	ctx := context.Background()
	if _, err := api.StatusWithoutPeers(ctx); err == nil {
		t.Error("StatusWithoutPeers must fail")
	}
	if _, err := api.WhoIs(ctx, "100.64.0.5"); err == nil {
		t.Error("WhoIs must fail")
	}
	if _, err := api.WatchIPNBus(ctx); err == nil {
		t.Error("WatchIPNBus must fail")
	}
}
