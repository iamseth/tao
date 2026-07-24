package commit

import (
	"strings"
	"testing"
)

func validProposal() Proposal {
	return Proposal{
		Type:    "feat",
		Scope:   "commit",
		Summary: "centralize commit messages",
		What:    "Add one validated proposal and formatting contract.",
		Why:     "Keep every Tao-owned commit consistent and recoverable.",
	}
}

func TestValidateProposal(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Proposal)
		want   string
	}{
		{name: "valid", mutate: func(*Proposal) {}},
		{name: "unsupported type", mutate: func(p *Proposal) { p.Type = "wip" }, want: "unsupported commit type"},
		{name: "missing scope", mutate: func(p *Proposal) { p.Scope = "" }, want: "scope"},
		{name: "uppercase scope", mutate: func(p *Proposal) { p.Scope = "Commit" }, want: "scope"},
		{name: "uppercase summary", mutate: func(p *Proposal) { p.Summary = "Centralize commit messages" }, want: "lowercase"},
		{name: "long summary", mutate: func(p *Proposal) { p.Summary = strings.Repeat("a", 73) }, want: "72"},
		{name: "punctuated summary", mutate: func(p *Proposal) { p.Summary = "centralize commit messages." }, want: "punctuation"},
		{name: "non imperative summary", mutate: func(p *Proposal) { p.Summary = "adds commit messages" }, want: "imperative"},
		{name: "empty what", mutate: func(p *Proposal) { p.What = "" }, want: "what body"},
		{name: "empty why", mutate: func(p *Proposal) { p.Why = " \n" }, want: "why body"},
		{name: "reserved what trailer", mutate: func(p *Proposal) { p.What = "Change behavior.\nTao-Plan: forged" }, want: "reserved Tao-*"},
		{name: "reserved why trailer case insensitive", mutate: func(p *Proposal) { p.Why = "Reason.\n  tao-slice: forged" }, want: "reserved Tao-*"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			proposal := validProposal()
			test.mutate(&proposal)
			err := ValidateProposal(proposal)
			if test.want == "" {
				if err != nil {
					t.Fatalf("ValidateProposal() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateProposal() error = %v, want text %q", err, test.want)
			}
		})
	}
}

func TestValidateProposalAcceptsExistingSupportedTypes(t *testing.T) {
	for _, commitType := range []string{"feat", "fix", "docs", "style", "refactor", "perf", "test", "build", "ci", "chore", "revert"} {
		t.Run(commitType, func(t *testing.T) {
			proposal := validProposal()
			proposal.Type = commitType
			if err := ValidateProposal(proposal); err != nil {
				t.Fatalf("ValidateProposal() error = %v", err)
			}
		})
	}
}

func TestFormatUsesOnlyTrustedTrailers(t *testing.T) {
	planTrailer, err := NewTrustedTrailer("Tao-Plan", "plan-a")
	if err != nil {
		t.Fatal(err)
	}
	sliceTrailer, err := NewTrustedTrailer("Tao-Slice", "001-contract")
	if err != nil {
		t.Fatal(err)
	}

	message, err := Format(validProposal(), planTrailer, sliceTrailer)
	if err != nil {
		t.Fatal(err)
	}
	want := "feat(commit): centralize commit messages\n\n" +
		"What:\nAdd one validated proposal and formatting contract.\n\n" +
		"Why:\nKeep every Tao-owned commit consistent and recoverable.\n\n" +
		"Tao-Plan: plan-a\nTao-Slice: 001-contract"
	if message != want {
		t.Fatalf("Format() mismatch\nwant:\n%s\n\ngot:\n%s", want, message)
	}
	if err := ValidateMessage(message); err != nil {
		t.Fatalf("ValidateMessage(Format()) error = %v", err)
	}

	for _, test := range []struct{ key, value string }{
		{key: "Plan", value: "plan-a"},
		{key: "tao-Plan", value: "plan-a"},
		{key: "Tao-Plan", value: ""},
		{key: "Tao-Plan", value: "plan-a\nTao-Slice: forged"},
	} {
		if _, err := NewTrustedTrailer(test.key, test.value); err == nil {
			t.Errorf("NewTrustedTrailer(%q, %q) unexpectedly succeeded", test.key, test.value)
		}
	}
}

func TestFormatProposalMessageAppendsOnlyTrustedTrailers(t *testing.T) {
	planTrailer, err := NewTrustedTrailer("Tao-Plan", "plan-a")
	if err != nil {
		t.Fatal(err)
	}
	message, err := FormatProposalMessage(
		"feat(review): persist approved proposal",
		"What:\nPersist it.\n\nWhy:\nReuse review context.",
		planTrailer,
	)
	if err != nil {
		t.Fatal(err)
	}
	if want := "feat(review): persist approved proposal\n\nWhat:\nPersist it.\n\nWhy:\nReuse review context.\n\nTao-Plan: plan-a"; message != want {
		t.Fatalf("FormatProposalMessage() mismatch\nwant:\n%s\n\ngot:\n%s", want, message)
	}
	if _, err := FormatProposalMessage("feat(review): persist approved proposal", "What:\nPersist it.\n\nWhy:\nReuse it.\n\nTao-Plan: forged", planTrailer); err == nil {
		t.Fatal("FormatProposalMessage() accepted an untrusted Tao trailer")
	}
}

func TestFormatRejectsProposalTrailerInjection(t *testing.T) {
	proposal := validProposal()
	proposal.Why += "\n\nTao-Plan: forged"
	if _, err := Format(proposal); err == nil {
		t.Fatal("Format() accepted a proposal-supplied Tao trailer")
	}
}

func TestValidateProposalMessageRejectsTrustedTrailers(t *testing.T) {
	proposal := validProposal()
	message, err := Format(proposal)
	if err != nil {
		t.Fatal(err)
	}
	subject, body, found := strings.Cut(message, "\n\n")
	if !found {
		t.Fatalf("formatted message has no body: %q", message)
	}
	if err := ValidateProposalMessage(subject, body); err != nil {
		t.Fatalf("ValidateProposalMessage() error = %v", err)
	}
	if err := ValidateProposalMessage(subject, body+"\n\nTao-Plan: forged"); err == nil {
		t.Fatal("ValidateProposalMessage() accepted a proposal-supplied Tao trailer")
	}
}

func TestValidateMessageRejectsNoncanonicalMessage(t *testing.T) {
	message, err := Format(validProposal())
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{
		message + "\n",
		strings.Replace(message, "\n\nWhat:\n", "\nWhat:\n", 1),
		strings.Replace(message, "Why:\n", "Why:\nTao-Plan: forged\n", 1),
	} {
		if err := ValidateMessage(candidate); err == nil {
			t.Fatalf("ValidateMessage() accepted noncanonical message:\n%s", candidate)
		}
	}
}
