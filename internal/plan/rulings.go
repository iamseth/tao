package plan

import (
	"strings"

	"github.com/iamseth/tao/internal/agentinput"
)

// Ruling limits bound advisory text extracted from agent-written slice notes.
const (
	// MaxSliceRulings bounds the number of rulings extracted from slice notes.
	MaxSliceRulings = 20
	// MaxSliceRulingRunes bounds each ruling, including its prefix.
	MaxSliceRulingRunes = 512
)

// SliceRulings extracts bounded, single-line agent-authored rulings in note order.
// Rulings retain their exact case-sensitive prefix and are advisory, not authority.
// Notes without non-empty rulings return nil.
func SliceRulings(notes string) []string {
	const prefix = "Ruling:"
	var rulings []string
	for line := range strings.SplitSeq(notes, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) || strings.TrimSpace(strings.TrimPrefix(line, prefix)) == "" {
			continue
		}
		rulings = append(rulings, agentinput.CapRunes(line, MaxSliceRulingRunes))
		if len(rulings) == MaxSliceRulings {
			break
		}
	}
	return rulings
}
