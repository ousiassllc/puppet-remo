//go:build windows

package windows

// NewSecretStore は資格情報マネージャー（go-keyring）による platform.SecretStore を作る。
// 資格情報はユーザーアカウントに束縛され、別のユーザーからは読めない（NFR-S14）。
func NewSecretStore() *SecretStore { return &SecretStore{} }
