package codex

import (
	"github.com/ousiassllc/puppet-remo/internal/codex/protocol"
	"github.com/ousiassllc/puppet-remo/internal/openai"
)

// DesktopToolSpec は thread/start の dynamicTools に渡す namespace desktop（openai.DesktopTools をそのまま関数にしたもの）。
func DesktopToolSpec() protocol.DynamicToolSpec {
	spec := protocol.DynamicToolSpec{
		Type: protocol.ToolTypeNamespace, Name: openai.DesktopNamespace, Description: openai.DesktopNamespaceDescription,
		Tools: make([]protocol.DynamicToolFunction, 0, len(openai.DesktopTools)),
	}
	for _, t := range openai.DesktopTools {
		spec.Tools = append(spec.Tools, protocol.DynamicToolFunction{
			Type: protocol.ToolTypeFunction, Name: t.Name, Description: t.Description, InputSchema: t.Parameters,
		})
	}
	return spec
}
