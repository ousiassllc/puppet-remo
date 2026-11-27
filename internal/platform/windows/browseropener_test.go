package windows

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ousiassllc/puppet-remo/internal/domain"
)

func TestCheckBrowserURL(t *testing.T) {
	ctx := context.Background()
	canceled, cancel := context.WithCancel(ctx)
	cancel()

	const secret = "id_token_hint=SECRET-HINT"
	tests := []struct {
		name string
		ctx  context.Context
		url  string
		want error // nil なら成功
	}{
		{"https", ctx, "https://auth.openai.com/api/accounts/authorize?client_id=x&" + secret, nil},
		{"https upper case scheme", ctx, "HTTPS://chatgpt.com/settings/usage", nil},
		{"empty", ctx, "", domain.ErrInvalidArgument},
		{"http", ctx, "http://auth.openai.com/?" + secret, domain.ErrInvalidArgument},
		{"file", ctx, "file:///C:/Windows/System32/calc.exe", domain.ErrInvalidArgument},
		{"a path", ctx, `C:\Windows\System32\calc.exe`, domain.ErrInvalidArgument},
		{"no host", ctx, "https:///path?" + secret, domain.ErrInvalidArgument},
		{"user info", ctx, "https://user:pass@auth.openai.com/?" + secret, domain.ErrInvalidArgument},
		{"NUL", ctx, "https://auth.openai.com/\x00?" + secret, domain.ErrInvalidArgument},
		{"space", ctx, "https://auth.openai.com/ x?" + secret, domain.ErrInvalidArgument},
		{"newline", ctx, "https://auth.openai.com/\n" + secret, domain.ErrInvalidArgument},
		{"canceled", canceled, "https://auth.openai.com/", context.Canceled},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checkBrowserURL(tc.ctx, tc.url)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("checkBrowserURL = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("checkBrowserURL = %v, want %v", err, tc.want)
			}
			if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "auth.openai.com") {
				t.Errorf("error leaks the url: %v", err)
			}
		})
	}
}
