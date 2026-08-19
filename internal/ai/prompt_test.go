package ai

import (
	"strings"
	"testing"
)

func TestWorkspaceSystemPromptExplainsPersistentMindMapRules(t *testing.T) {
	prompt := WorkspaceSystemPrompt("Fotossíntese", "(nenhum arquivo ainda)", false)
	for _, want := range []string{
		"get_mind_map",
		"parent_id",
		"raiz central",
		"branches distribuídos radialmente",
		"parede de cartões",
		"rótulos curtos",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}
