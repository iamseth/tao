package promptinstall

import (
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/agent/promptfmt"
)

func TestManagedClaudeCommandWrapper(t *testing.T) {
	text, err := promptfmt.ManagedClaudeCommand("plan", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"description: Tao /plan command wrapper", "allowed-tools: Bash(tao prompt plan:*)", "tao-managed: plan v1", "```!", "tao prompt plan --arguments-stdin <<'TAO_PROMPT_ARGUMENTS'", "$ARGUMENTS", "TAO_PROMPT_ARGUMENTS"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in Claude command wrapper, got %q", want, text)
		}
	}
	if strings.Contains(text, "{{ .Arguments }}") {
		t.Fatalf("expected no embedded template content in Claude command wrapper, got %q", text)
	}
}

func TestManagedPiTemplateFrontmatterVariants(t *testing.T) {
	plain, err := promptfmt.ManagedPiTemplate("plain", "Body {{.Arguments}}")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plain, "<!-- tao-managed: plain v1 -->") || !strings.Contains(plain, "$ARGUMENTS") {
		t.Fatalf("unexpected plain template %q", plain)
	}
	malformed, err := promptfmt.ManagedPiTemplate("bad", "---\ntitle: bad\nBody")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(malformed, "<!-- tao-managed: bad v1 -->") {
		t.Fatalf("expected malformed frontmatter to get leading marker, got %q", malformed)
	}
	crlf, err := promptfmt.ManagedPiTemplate("crlf", "---\ntitle: ok\n---\r\nBody")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(crlf, "---\r\n\n<!-- tao-managed: crlf v1 -->\n\nBody") {
		t.Fatalf("expected marker after CRLF frontmatter, got %q", crlf)
	}
}
