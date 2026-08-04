package commit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// MaxProposalFileBytes bounds one agent-written structured proposal file.
const MaxProposalFileBytes int64 = 32 * 1024

// DecodeProposal strictly decodes exactly one JSON proposal object and
// validates the complete untrusted proposal contract. Unknown fields, trailing
// JSON values, and contract violations are errors so an invalid proposal stops
// before any commit intent exists.
func DecodeProposal(data []byte) (*Proposal, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var proposal Proposal
	if err := decoder.Decode(&proposal); err != nil {
		return nil, fmt.Errorf("decode commit proposal: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("decode commit proposal: multiple JSON values")
		}
		return nil, fmt.Errorf("decode commit proposal: %w", err)
	}
	if err := ValidateProposal(proposal); err != nil {
		return nil, fmt.Errorf("validate commit proposal: %w", err)
	}
	return &proposal, nil
}
