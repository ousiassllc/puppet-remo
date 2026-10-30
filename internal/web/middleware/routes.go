package middleware

import (
	"net/http"
	"path"
	"strings"

	"github.com/ousiassllc/puppet-remo/internal/service"
)

// routeKind は経路ごとのエラー形式と、認可で使う機能を決める。
type routeKind int

const (
	routeWeb routeKind = iota // Web UI・静的ファイル（HTML で返す）
	routeAPI                  // /api/v1/*（problem+json）
	routeWS                   // /ws/*（ハンドシェイク前は problem+json）
	routeMCP                  // /mcp（problem+json。Bearer 必須）
)

// classify は URL パスから経路の種別を返す。パスは正規化してから判定する
// （ServeMux が後で正規化して /mcp に振り分けても、検査を迂回できないようにする）。
func classify(r *http.Request) routeKind {
	p := path.Clean("/" + r.URL.Path)
	switch {
	case under(p, "/mcp"):
		return routeMCP
	case under(p, "/api/v1"):
		return routeAPI
	case under(p, "/ws"):
		return routeWS
	}
	return routeWeb
}

func under(p, prefix string) bool {
	return p == prefix || strings.HasPrefix(p, prefix+"/")
}

// problem は problem+json で応答する経路か。
func (k routeKind) problem() bool { return k != routeWeb }

// feature は経路が使うクライアント機能。
func (k routeKind) feature() service.Feature {
	if k == routeMCP {
		return service.FeatureMCP
	}
	return service.FeatureWeb
}

// stateChanging はリクエストが状態を変更しうるメソッドか。
func stateChanging(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}
