package domain

import "time"

// Clock は現在時刻の供給元。テストでは固定・進行可能な実装に差し替える。
type Clock interface {
	Now() time.Time
}

// IDGen は一意な ID（リース ID など）の供給元。
type IDGen interface {
	NewID() string
}
