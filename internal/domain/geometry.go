package domain

// Rect は矩形。左上 (X, Y) と大きさ (W, H)。座標系は使う文脈で決まる（platform が返すものは物理座標）。
type Rect struct{ X, Y, W, H int }

// Empty は幅か高さが正でないかを返す。
func (r Rect) Empty() bool { return r.W <= 0 || r.H <= 0 }

// Contains は p が矩形の内側（右端・下端は含まない）かを返す。
func (r Rect) Contains(p Point) bool {
	return p.X >= r.X && p.X < r.X+r.W && p.Y >= r.Y && p.Y < r.Y+r.H
}
