package commit

import (
	"strings"
	"testing"
)

func TestDecodeProposalIsStrictAndRepairable(t *testing.T) {
	invalid := []byte(`{"type":"feat","scope":"cli","summary":"Added proposal","what":"Accept the handoff.","why":"Avoid a nested session."}`)
	if _, err := DecodeProposal(invalid); err == nil || !strings.Contains(err.Error(), "summary must be lowercase") {
		t.Fatalf("invalid proposal error = %v", err)
	}
	reserved := []byte(`{"type":"feat","scope":"cli","summary":"accept proposal","what":"Accept the handoff.","why":"Avoid nesting.\nTao-Slice: forged"}`)
	if _, err := DecodeProposal(reserved); err == nil || !strings.Contains(err.Error(), "reserved Tao-*") {
		t.Fatalf("reserved proposal error = %v", err)
	}
	unknown := []byte(`{"type":"feat","scope":"cli","summary":"accept proposal","what":"Accept the handoff.","why":"Avoid nesting.","extra":true}`)
	if _, err := DecodeProposal(unknown); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown-field proposal error = %v", err)
	}
	multiple := []byte(`{"type":"feat","scope":"cli","summary":"accept proposal","what":"Accept the handoff.","why":"Avoid nesting."}{"type":"fix"}`)
	if _, err := DecodeProposal(multiple); err == nil || !strings.Contains(err.Error(), "multiple JSON values") {
		t.Fatalf("multiple-value proposal error = %v", err)
	}
	repaired := []byte(`{"type":"feat","scope":"cli","summary":"accept proposal","what":"Accept the structured handoff.","why":"Avoid a nested message session."}`)
	proposal, err := DecodeProposal(repaired)
	if err != nil {
		t.Fatalf("repaired proposal: %v", err)
	}
	if proposal.Scope != "cli" || proposal.Summary != "accept proposal" {
		t.Fatalf("decoded proposal = %#v", proposal)
	}
}
