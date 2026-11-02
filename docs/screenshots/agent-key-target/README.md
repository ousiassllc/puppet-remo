# /agent の API キーの保存先の表示

資格情報マネージャー上の実際のターゲット名（go-keyring が `service:user` で作る `puppet-remo/openai:puppet-remo`・`puppet-remo/typesafe:puppet-remo`）に表示を合わせた。
`task gui:demo` で撮影（架空のデータ）。

| ファイル | 内容 |
|---------|------|
| `agent-key-target-before.png` | 修正前。`puppet-remo/openai`・`puppet-remo/typesafe` と表示（資格情報マネージャーで探しても見つからない名前） |
| `agent-key-target-after.png` | 修正後。実際の名前を表示 |
