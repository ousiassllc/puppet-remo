package app

import (
	"slices"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/service"
)

// Listener が先に Listen を始め、StatusTracker がまだ新しい状態を受け取っていない間（TailnetIPs が空）も、Listen しているアドレスの Host は許す。
// 許さないと、その間に届いた正当な要求（MCP の initialize など）が 400 invalid_host になる（CI で不安定だった）。
func TestHostsOfAllowsListenAddrsBeforeTheTrackerSeesTheStatus(t *testing.T) {
	got := hostsOf(service.ServerStatus{ListenAddrs: []string{"100.64.0.1:8765", "[fd7a:115c:a1e0::1]:8765"}})
	for _, want := range []string{"100.64.0.1", "fd7a:115c:a1e0::1"} {
		if !slices.Contains(got, want) {
			t.Errorf("hostsOf = %v, want it to contain %s", got, want)
		}
	}
}

func TestHostsOfDoesNotDuplicateOrAddUnrelatedHosts(t *testing.T) {
	got := hostsOf(service.ServerStatus{TailnetIPs: []string{"100.64.0.1"}, ListenAddrs: []string{"100.64.0.1:8765"}, DNSName: "host.tailnet.ts.net."})
	if want := []string{"100.64.0.1", "host.tailnet.ts.net."}; !slices.Equal(got, want) {
		t.Errorf("hostsOf = %v, want %v", got, want)
	}
	if got := hostsOf(service.ServerStatus{ListenAddrs: []string{"not-an-address"}}); len(got) != 0 {
		t.Errorf("a malformed listen address must not be allowed: %v", got)
	}
}
