package promptinstall

import (
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/agent/promptfmt"
)

func TestManagedClaudeCommandWrapper(t *testing.T) {
	text, err := promptfmt.ManagedClaudeCommand("tao-plan", "plan", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"description: Tao /tao-plan command wrapper", "allowed-tools: Bash(tao prompt plan:*)", "tao-managed: tao-plan v1", "```!", "tao prompt plan --arguments-stdin <<'TAO_PROMPT_ARGUMENTS'", "$ARGUMENTS", "TAO_PROMPT_ARGUMENTS"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in Claude command wrapper, got %q", want, text)
		}
	}
	if strings.Contains(text, "{{ .Arguments }}") {
		t.Fatalf("expected no embedded template content in Claude command wrapper, got %q", text)
	}
}

func TestInlineTemplatesPreserveNoteSourceMetadata(t *testing.T) {
	template := "---\ndescription: note aware\nagent: plan\n---\n\n## Source Note\n\n- ID: `<canonical note ID>`\n\n{{ .Arguments }}\n"
	pi, err := promptfmt.ManagedPiTemplate("tao-plan", "plan", template)
	if err != nil {
		t.Fatal(err)
	}
	inline, err := promptfmt.ManagedInlinePrompt("tao-plan", "plan", template)
	if err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{"pi": pi, "inline": inline} {
		for _, want := range []string{"## Source Note", "- ID: `<canonical note ID>`", "$ARGUMENTS"} {
			if !strings.Contains(text, want) {
				t.Fatalf("%s installed prompt missing %q: %q", name, want, text)
			}
		}
		if strings.Contains(text, "{{ .Arguments }}") {
			t.Fatalf("%s installed prompt retained Go template syntax: %q", name, text)
		}
	}
}

func TestManagedPiTemplateFrontmatterVariants(t *testing.T) {
	plain, err := promptfmt.ManagedPiTemplate("tao-plain", "plain", "Body {{.Arguments}}")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plain, "<!-- tao-managed: tao-plain v1 -->") || !strings.Contains(plain, "$ARGUMENTS") {
		t.Fatalf("unexpected plain template %q", plain)
	}
	malformed, err := promptfmt.ManagedPiTemplate("tao-bad", "bad", "---\ntitle: bad\nBody")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(malformed, "<!-- tao-managed: tao-bad v1 -->") {
		t.Fatalf("expected malformed frontmatter to get leading marker, got %q", malformed)
	}
	crlf, err := promptfmt.ManagedPiTemplate("tao-crlf", "crlf", "---\ntitle: ok\n---\r\nBody")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(crlf, "---\r\n\n<!-- tao-managed: tao-crlf v1 -->\n\nBody") {
		t.Fatalf("expected marker after CRLF frontmatter, got %q", crlf)
	}
}
