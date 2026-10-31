package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLimitBody(t *testing.T) {
	const max = 8
	var gotBody string
	var reached bool
	h := LimitBody(max)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		if r.ContentLength != int64(len(b)) {
			t.Errorf("ContentLength = %d, want %d", r.ContentLength, len(b))
		}
	}))
	tests := []struct {
		name        string
		method      string
		body        string
		chunked     bool
		wantStatus  int
		wantReached bool
	}{
		{"at the limit", http.MethodPost, strings.Repeat("a", max), false, http.StatusOK, true},
		{"over the limit", http.MethodPost, strings.Repeat("a", max+1), false, http.StatusRequestEntityTooLarge, false},
		{"over the limit without Content-Length", http.MethodPut, strings.Repeat("a", max+1), true, http.StatusRequestEntityTooLarge, false},
		{"delete over the limit", http.MethodDelete, strings.Repeat("a", max+1), false, http.StatusRequestEntityTooLarge, false},
		{"empty body", http.MethodPost, "", false, http.StatusOK, true},
		{"GET is not limited", http.MethodGet, strings.Repeat("a", max+1), false, http.StatusOK, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reached, gotBody = false, ""
			req := httptest.NewRequest(tt.method, "/x", strings.NewReader(tt.body))
			if tt.chunked {
				req.ContentLength = -1
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus || reached != tt.wantReached {
				t.Fatalf("status %d reached %v, want %d %v", rec.Code, reached, tt.wantStatus, tt.wantReached)
			}
			if tt.wantReached && tt.method != http.MethodGet && gotBody != tt.body {
				t.Errorf("handler body = %q, want %q", gotBody, tt.body)
			}
		})
	}
}
