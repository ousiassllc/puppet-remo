package service

import (
	"reflect"
	"slices"
	"testing"
)

// Control が受け取る AppReader は読み取り（Get・List）だけで、許可リストへの書き込みの経路を型の上で持たない（NFR-S20 (2)）。
// メソッドが 1 つ増えると、読み取り専用かどうかをここで見直すことになる（足す場合はこのリストも更新する）。
func TestAppReaderIsReadOnly(t *testing.T) {
	reader := reflect.TypeFor[AppReader]()
	var got []string
	for m := range reader.Methods() {
		got = append(got, m.Name)
	}
	slices.Sort(got)
	if want := []string{"Get", "List"}; !slices.Equal(got, want) {
		t.Errorf("AppReader methods = %v, want exactly %v", got, want)
	}

	// 書き込みのメソッドは AppRegistry にだけある（AppReader から外れたことで、上の検査が空振りしていない）。
	registry := reflect.TypeFor[AppRegistry]()
	for _, name := range []string{"Create", "Update", "Delete", "PickExecutable"} {
		if _, ok := reader.MethodByName(name); ok {
			t.Errorf("AppReader has the write method %s", name)
		}
		if _, ok := registry.MethodByName(name); !ok {
			t.Errorf("AppRegistry lacks %s (the write surface moved?)", name)
		}
	}
}
