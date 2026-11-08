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
		{name: "reserved verification command", mutate: func(p *Proposal) { p.What = "Change behavior.\nTao-Verify-Command: forged" }, want: "reserved Tao-*"},
		{name: "reserved verification exit", mutate: func(p *Proposal) { p.Why = "Reason.\nTao-Verify-Exit: 1: 0" }, want: "reserved Tao-*"},
		{name: "reserved verification digest", mutate: func(p *Proposal) { p.Why = "Reason.\n  tao-verify-digest: forged" }, want: "reserved Tao-*"},
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

func TestProposalBodyTrailerBoundary(t *testing.T) {
	cases := []struct {
		name     string
		line     string
		reserved bool
	}{
		{"existing prose", "Keep every Tao-owned commit consistent and recoverable.", false},
		{"leading prose", "Tao-owned commit consistent and recoverable.", false},
		{"gates prose", "Tao-owned gates protect commits: keep them enabled.", false},
		{"space before colon", "Tao-Plan : prose", false},
		{"nonletter key", "Tao-1Plan: prose", false},
		{"empty key", "Tao-: prose", false},
		{"invalid key character", "Tao-Plan_Name: prose", false},
		{"reviewed by", "Tao-Reviewed-By: x", true},
		{"lowercase", "tao-plan: y", true},
		{"mixed case", "tAo-pLaN: y", true},
		{"indented", " \tTao-Plan: y", true},
		{"trailing whitespace", "Tao-Plan: y \t", true},
		{"empty value", "Tao-Plan:", true},
		{"whitespace value", "Tao-Plan: \t", true},
		{"no space", "Tao-Plan:y", true},
		{"key digits and hyphens", "TAO-A1-B2: y", true},
	}
	for _, tc := range cases {
		for _, section := range []string{"what", "why"} {
			for _, position := range []string{"initial", "internal", "final paragraph"} {
				t.Run(tc.name+"/"+section+"/"+position, func(t *testing.T) {
					text := tc.line + "\nContinue the explanation."
					switch position {
					case "internal":
						text = "Start the explanation.\n" + text
					case "final paragraph":
						text = "Start the explanation.\n\n" + tc.line
					}
					// Keep body-edge trimming independent of the reserved-line check.
					if text != strings.TrimSpace(text) {
						t.Skip("body-edge whitespace is invalid independently; covered on internal lines")
					}
					p := validProposal()
					if section == "what" {
						p.What = text
					} else {
						p.Why = text
					}
					// Construct untrusted input independently of the trusted formatter.
					subject := "feat(commit): centralize commit messages"
					body := "What:\n" + p.What + "\n\nWhy:\n" + p.Why
					t.Run("structured", func(t *testing.T) {
						checkProposalTrailerBoundary(t, ValidateProposal(p), tc.reserved)
					})
					t.Run("canonical", func(t *testing.T) {
						checkProposalTrailerBoundary(t, ValidateProposalMessage(subject, body), tc.reserved)
					})
				})
			}
		}
	}
}

func checkProposalTrailerBoundary(t *testing.T, err error, reserved bool) {
	t.Helper()
	if reserved {
		if err == nil || err.Error() != "proposal must not contain reserved Tao-* lines" {
			t.Fatalf("error = %v, want reserved trailer error", err)
		}
	} else if err != nil {
		t.Fatalf("prose rejected: %v", err)
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

func TestFormatTaoOwnedProseWithTrustedTrailers(t *testing.T) {
	p := validProposal()
	p.What = "Tao-owned gates protect commits.\nKeep the checks enabled."
	p.Why = "Keep every Tao-owned commit consistent and recoverable.\n\nTao-owned evidence stays separate."
	subject := "feat(commit): centralize commit messages"
	body := "What:\nTao-owned gates protect commits.\nKeep the checks enabled.\n\nWhy:\nKeep every Tao-owned commit consistent and recoverable.\n\nTao-owned evidence stays separate."
	trailer, err := NewTrustedTrailer("Tao-Plan", "plan-a")
	if err != nil {
		t.Fatal(err)
	}
	for _, trusted := range []bool{false, true} {
		var trailers []TrustedTrailer
		want := subject + "\n\n" + body
		if trusted {
			trailers = append(trailers, trailer)
			want += "\n\nTao-Plan: plan-a"
		}
		for name, format := range map[string]func() (string, error){
			"Format":                func() (string, error) { return Format(p, trailers...) },
			"FormatProposalMessage": func() (string, error) { return FormatProposalMessage(subject, body, trailers...) },
		} {
			message, err := format()
			if err != nil {
				t.Fatalf("%s(trusted=%t): %v", name, trusted, err)
			}
			if message != want {
				t.Fatalf("%s(trusted=%t) = %q, want %q", name, trusted, message, want)
			}
			if err := ValidateMessage(message); err != nil {
				t.Fatalf("ValidateMessage(%s(trusted=%t)): %v", name, trusted, err)
			}
		}
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
