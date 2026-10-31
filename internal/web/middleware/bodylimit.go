package middleware

import (
	"bytes"
	"errors"
	"io"
	"net/http"
)

// LimitBody は、状態変更（GET / HEAD / OPTIONS 以外）のリクエスト本文を max バイトまでにする。超えたら 413 を返し、
// 次のハンドラを呼ばない（フォームが途中で切れて、欠けた値で保存されることを避けるため、本文は先に最後まで読む）。
// 本文は max バイト以下のときだけ次へ渡す（メモリに載せるのも max まで）。
func LimitBody(max int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !stateChanging(r) || r.Body == nil || r.Body == http.NoBody {
				next.ServeHTTP(w, r)
				return
			}
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, max))
			if err != nil {
				var tooLarge *http.MaxBytesError
				if errors.As(err, &tooLarge) {
					http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				} else {
					http.Error(w, "bad request", http.StatusBadRequest)
				}
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
			next.ServeHTTP(w, r)
		})
	}
}
