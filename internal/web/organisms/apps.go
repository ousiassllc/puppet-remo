package organisms

import "github.com/ousiassllc/puppet-remo/internal/web/view"

func appFormMode(f view.AppForm) string {
	if f.ID == "" {
		return "new"
	}
	return "edit"
}

func appFormTitle(f view.AppForm) string {
	if f.ID == "" {
		return "アプリを追加"
	}
	return "アプリを編集"
}

func appFormSubmitLabel(f view.AppForm) string {
	if f.ID == "" {
		return "追加"
	}
	return "保存"
}

func appCardID(a view.AppRow) string { return "app-" + a.ID }
