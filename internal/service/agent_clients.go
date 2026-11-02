package service

import (
	"context"

	"github.com/ousiassllc/puppet-remo/internal/openai"
	"github.com/ousiassllc/puppet-remo/internal/platform"
	"github.com/ousiassllc/puppet-remo/internal/typesafe"
)

// このファイルは、エージェントが呼ぶ外部サービス（OpenAI・TypeSafe）のクライアントを組み立てる窓口。
// app は openai・typesafe を import できない（scripts/check-imports.sh。docs §1）ので、組み立てと型の別名はここに置く
// （repository の型の別名 ConnRequest などと同じ扱い。docs §13.1）。

// AgentModel は OpenAI の Responses API の窓口（openai.Model の別名）。
type AgentModel = openai.Model

// AgentChooser は TypeSafe（Jev）の窓口（typesafe.Chooser の別名）。
type AgentChooser = typesafe.Chooser

// NewAgentModel は本物の OpenAI クライアントを返す。キーは SecretStore の SecretOpenAI を呼び出しのたびに読む（保持しない。NFR-S14）。
// 組み立てるだけで、通信も goroutine も起動しない。
func NewAgentModel(secrets platform.SecretStore) AgentModel {
	return openai.New(secretReader(secrets, platform.SecretOpenAI), openai.Config{})
}

// NewAgentChooser は本物の TypeSafe クライアントを返す。キーは SecretStore の SecretTypeSafe を呼び出しのたびに読む（保持しない）。
// 組み立てるだけで、通信も goroutine も起動しない。
func NewAgentChooser(secrets platform.SecretStore) AgentChooser {
	return typesafe.New(secretReader(secrets, platform.SecretTypeSafe), typesafe.Config{})
}

// secretReader は SecretStore の name を呼び出しのたびに読む関数（openai.New・typesafe.New の key）。値は保持せず、エラーに値を含めない。
func secretReader(s platform.SecretStore, name string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return s.Get(name) }
}
