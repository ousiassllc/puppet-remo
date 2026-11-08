package domain

// ビューア（#157）のウィンドウ一覧。選べるのは、所有されていない最上位のウィンドウだけ（T の候補）。
// ダイアログ・メニューなどの関連ウィンドウは、T を選ぶと自動で対象グループ G に入るので、一覧には出さない。

// PickCandidate は一覧の 1 件。Reason が空でなければ選べない（灰色で表示する理由。束縛が断る理由と同じコード）。
type PickCandidate struct {
	Window WindowInfo
	Ref    WindowRef
	// Reason は選べない理由: ReasonElevatedWindow（昇格）/ ReasonTargetMinimized / ReasonTargetUnsupported（UWP など同一性を確かめられない）。
	Reason GroupReason
}

// Selectable は選べるかを返す。
func (c PickCandidate) Selectable() bool { return c.Reason == "" }

// PickCandidates は windows（WindowManager.ListAll の結果。前面が先）から一覧に出すウィンドウを、その順で返す純粋関数。
// 出さないもの: 自プロセスのウィンドウ（オーバーレイを含む）、タイトルのないもの、メニュー、所有されている・子のウィンドウ（関連ウィンドウ）。
func PickCandidates(windows []WindowInfo) []PickCandidate {
	var out []PickCandidate
	for _, w := range windows {
		if w.Own || w.Title == "" || w.Class == ClassMenu || w.MenuOwnerID != "" || len(w.OwnerChain) > 0 || w.Style.Child {
			continue
		}
		c := PickCandidate{Window: w, Ref: RefOf(w)}
		switch {
		case w.Elevated:
			c.Reason = ReasonElevatedWindow
		case w.Class == ClassApplicationFrameWindow || !c.Ref.Known():
			c.Reason = ReasonTargetUnsupported
		case w.Minimized:
			c.Reason = ReasonTargetMinimized
		}
		out = append(out, c)
	}
	return out
}
