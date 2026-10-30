package platform

// Power はスリープ抑止。
type Power interface {
	KeepAwake(on bool)
}
