package protocol

import (
	"bytes"
	"encoding/json"
)

// アプリが区別する codexErrorInfo の種類。データを持たない種類は文字列、持つものは 1 つのキーのオブジェクト
// （{"httpConnectionFailed":{"httpStatusCode":502}}）で届く。どちらも ErrorInfo.Kind に読む。
const (
	ErrContextWindowExceeded = "contextWindowExceeded"
	ErrUsageLimitExceeded    = "usageLimitExceeded"
	ErrUnauthorized          = "unauthorized"
	ErrOther                 = "other"
)

// ErrorInfo は codexErrorInfo。ゼロ値は「情報なし」。
type ErrorInfo struct {
	Kind       string
	HTTPStatus int
}

// UnmarshalJSON は文字列の形と 1 キーのオブジェクトの形を読む。それ以外は Kind が other。
func (e *ErrorInfo) UnmarshalJSON(b []byte) error {
	*e = ErrorInfo{}
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		return nil
	}
	var s string
	if json.Unmarshal(b, &s) == nil {
		e.Kind = s
		return nil
	}
	var obj map[string]struct {
		HTTPStatusCode *int `json:"httpStatusCode"`
	}
	if json.Unmarshal(b, &obj) != nil || len(obj) != 1 {
		e.Kind = ErrOther
		return nil
	}
	for k, v := range obj {
		e.Kind = k
		if v.HTTPStatusCode != nil {
			e.HTTPStatus = *v.HTTPStatusCode
		}
	}
	return nil
}
