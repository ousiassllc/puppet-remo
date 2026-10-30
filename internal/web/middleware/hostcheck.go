package middleware

import (
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

// HostSource は Host ヘッダーとして許す tailnet IP・MagicDNS 名と、設定ポートを返す。
// tailnet の状態や設定が変わっても追従できるよう、リクエストごとに呼ぶ。
type HostSource func() (hosts []string, port int)

// HostCheck は Host ヘッダー（tailnet IP / MagicDNS 名 + 設定ポート以外は 400 invalid_host）と、
// Origin ヘッダー（同一オリジン以外は 403 invalid_origin）を検証する（NFR-S02。DNS リバインディング対策）。
func HostCheck(src HostSource) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hosts, port := src()
			if !hostAllowed(r.Host, hosts, port) {
				writeReject(w, r, classify(r), reject{code: domain.CodeInvalidHost})
				return
			}
			if !sameOrigin(r) {
				writeReject(w, r, classify(r), reject{code: domain.CodeInvalidOrigin})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// hostAllowed は Host ヘッダー値が「許可する host + 設定ポート」かを返す。
// ポートの省略は、設定ポートが 80 のときだけ許す。
func hostAllowed(header string, hosts []string, port int) bool {
	host, p, err := net.SplitHostPort(header)
	if err != nil {
		if port != 80 {
			return false
		}
		host = header
		if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
			host = host[1 : len(host)-1]
		} else if strings.Contains(host, ":") {
			return false
		}
	} else if p != strconv.Itoa(port) {
		return false
	}
	if host == "" {
		return false
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.Zone() != "" {
			return false
		}
		ip = ip.Unmap()
		for _, h := range hosts {
			if a, err := netip.ParseAddr(h); err == nil && a.WithZone("").Unmap() == ip {
				return true
			}
		}
		return false
	}
	name := normalizeName(host)
	for _, h := range hosts {
		if _, err := netip.ParseAddr(h); err != nil && normalizeName(h) == name {
			return true
		}
	}
	return false
}

func normalizeName(s string) string { return strings.TrimSuffix(strings.ToLower(s), ".") }

// sameOrigin は Origin ヘッダーが無い、またはリクエスト先（検証済みの Host）と同一オリジンかを返す。
func sameOrigin(r *http.Request) bool {
	origins := r.Header.Values("Origin")
	switch len(origins) {
	case 0:
		return true
	case 1:
	default:
		return false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	// 「scheme://host」とだけ一致させる（"null"・パス付き・ユーザー情報付きは一致しない）。
	o := origins[0]
	rest, ok := strings.CutPrefix(o, scheme+"://")
	return ok && rest != "" && strings.EqualFold(rest, r.Host)
}
