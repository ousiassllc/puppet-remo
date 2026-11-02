package domain_test

import (
	"reflect"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

func TestOwnWindowRegions(t *testing.T) {
	monitors := []domain.Monitor{fhd, hd} // fhd は縮小 0.8167、hd は右隣で縮小なし
	tests := []struct {
		name string
		wins []domain.WindowInfo
		want []domain.OwnRegion
	}{
		{"自ウィンドウがなければ nil", []domain.WindowInfo{{ID: "1", Rect: domain.Rect{W: 100, H: 100}}}, nil},
		{"他アプリ・昇格ウィンドウは含めない", []domain.WindowInfo{{ID: "1", Elevated: true, Rect: domain.Rect{W: 100, H: 100}}}, nil},
		{"縮小モニター", []domain.WindowInfo{{Own: true, Rect: domain.Rect{X: 960, Y: 540, W: 960, H: 540}}},
			[]domain.OwnRegion{{Monitor: 0, Rect: domain.Rect{X: 784, Y: 441, W: 784, H: 441}}}},
		{"非プライマリ・縮小なし", []domain.WindowInfo{{Own: true, Rect: domain.Rect{X: 1920 + 100, Y: 50, W: 200, H: 100}}},
			[]domain.OwnRegion{{Monitor: 1, Rect: domain.Rect{X: 100, Y: 50, W: 200, H: 100}}}},
		{"2 台にまたがるウィンドウはモニターごとに切り詰める", []domain.WindowInfo{{Own: true, Rect: domain.Rect{X: 1820, Y: 0, W: 200, H: 100}}},
			[]domain.OwnRegion{
				{Monitor: 0, Rect: domain.Rect{X: 1486, Y: 0, W: 82, H: 82}},
				{Monitor: 1, Rect: domain.Rect{X: 0, Y: 0, W: 100, H: 100}},
			}},
		{"最小化・どのモニターにもないウィンドウは含めない", []domain.WindowInfo{
			{Own: true, Minimized: true, Rect: domain.Rect{W: 100, H: 100}},
			{Own: true, Rect: domain.Rect{X: -32000, Y: -32000, W: 160, H: 28}},
			{Own: true, Rect: domain.Rect{X: 5000, Y: 0, W: 100, H: 100}},
		}, nil},
		{"クリックスルーのオーバーレイは含めない（全モニターを覆っても避ける範囲ではない）", []domain.WindowInfo{
			{Own: true, Overlay: true, Rect: domain.Rect{X: 0, Y: 0, W: 1920, H: 1080}},
			{Own: true, Rect: domain.Rect{X: 1920, Y: 0, W: 100, H: 100}},
		}, []domain.OwnRegion{{Monitor: 1, Rect: domain.Rect{X: 0, Y: 0, W: 100, H: 100}}}},
		{"複数の自ウィンドウは入力の順", []domain.WindowInfo{
			{Own: true, Rect: domain.Rect{X: 1920, Y: 0, W: 100, H: 100}},
			{Own: true, Rect: domain.Rect{X: 0, Y: 0, W: 1920, H: 1080}},
		}, []domain.OwnRegion{
			{Monitor: 1, Rect: domain.Rect{X: 0, Y: 0, W: 100, H: 100}},
			{Monitor: 0, Rect: domain.Rect{X: 0, Y: 0, W: 1568, H: 882}},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := domain.OwnWindowRegions(tt.wins, monitors, defaultMaxDim)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("OwnWindowRegions = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestInputBlockReason(t *testing.T) {
	tests := []struct {
		w    domain.WindowInfo
		want string
	}{
		{domain.WindowInfo{}, ""},
		{domain.WindowInfo{Own: true}, domain.ReasonOwnWindow},
		{domain.WindowInfo{Elevated: true}, domain.ReasonElevatedWindow},
		{domain.WindowInfo{Own: true, Elevated: true}, domain.ReasonOwnWindow},
		{domain.WindowInfo{Own: true, Overlay: true}, ""},
		{domain.WindowInfo{Own: true, Overlay: true, Elevated: true}, ""},
	}
	for _, tt := range tests {
		if got := domain.InputBlockReason(tt.w); got != tt.want {
			t.Errorf("InputBlockReason(%+v) = %q, want %q", tt.w, got, tt.want)
		}
	}
}
