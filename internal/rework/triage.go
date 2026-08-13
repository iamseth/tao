package rework

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/iamseth/tao/prompts"
)

const (
	maxPRTriageOutputBytes     = 64 * 1024
	maxPRTriageClassifications = 200
	maxPRTriageNodeIDRunes     = 256
	maxPRTriageRationaleRunes  = 512
)

// PRThreadKind is the bounded classification vocabulary for one review thread.
type PRThreadKind string

const (
	PRThreadKindChange     PRThreadKind = "change"
	PRThreadKindQuestion   PRThreadKind = "question"
	PRThreadKindScope      PRThreadKind = "scope"
	PRThreadKindUnmappable PRThreadKind = "unmappable"
)

// PRThreadClassification is one centrally validated agent classification.
type PRThreadClassification struct {
	ThreadNodeID string       `json:"thread_node_id"`
	Kind         PRThreadKind `json:"kind"`
	Rationale    string       `json:"rationale"`
}

// PRTriageTextGenerator is the provider-neutral boundary for one triage agent
// session. Implementations must return the agent's final structured text.
type PRTriageTextGenerator interface {
	GenerateText(context.Context, string, string) (string, error)
}

// PRTriageTextGeneratorFunc adapts a function to PRTriageTextGenerator.
type PRTriageTextGeneratorFunc func(context.Context, string, string) (string, error)

func (f PRTriageTextGeneratorFunc) GenerateText(ctx context.Context, repoRoot, prompt string) (string, error) {
	return f(ctx, repoRoot, prompt)
}

// PRThreadClassifier shapes one bounded prompt and validates one agent result.
// Slice generation and lifecycle gates deliberately do not depend on it.
type PRThreadClassifier struct {
	Text PRTriageTextGenerator
}

// Classify invokes one agent session and returns classifications in the same
// order as the requested threads. It has no retry or fallback behavior.
func (c PRThreadClassifier) Classify(ctx context.Context, repoRoot string, threads []PRThread) ([]PRThreadClassification, error) {
	if c.Text == nil {
		return nil, errors.New("pull-request thread classifier is not configured")
	}
	if strings.TrimSpace(repoRoot) == "" {
		return nil, errors.New("pull-request thread triage requires a repo root")
	}
	ids, err := requestedPRThreadIDs(threads)
	if err != nil {
		return nil, err
	}
	if len(threads) == 0 {
		return nil, nil
	}

	prose := make([]string, 0, len(threads))
	for _, thread := range threads {
		packet, err := json.Marshal(threadPacket(thread))
		if err != nil {
			return nil, fmt.Errorf("encode pull-request thread %q: %w", thread.NodeID, err)
		}
		prose = append(prose, string(packet))
	}
	packets, err := prompts.RenderPRThreadPackets(prose)
	if err != nil {
		return nil, err
	}
	prompt, err := prompts.RenderReworkTriage(prompts.ReworkTriageData{ThreadCount: len(threads), ThreadPackets: packets})
	if err != nil {
		return nil, fmt.Errorf("render pull-request thread triage prompt: %w", err)
	}
	output, err := c.Text.GenerateText(ctx, repoRoot, prompt)
	if err != nil {
		return nil, fmt.Errorf("classify pull-request threads: %w", err)
	}
	return DecodePRTriageResult([]byte(output), ids)
}

// DecodePRTriageResult strictly decodes and validates a complete classification
// set. Malformed, partial, duplicate, and out-of-vocabulary output is refused.
func DecodePRTriageResult(data []byte, requestedThreadIDs []string) ([]PRThreadClassification, error) {
	if len(data) > maxPRTriageOutputBytes {
		return nil, fmt.Errorf("pull-request thread triage output exceeds %d bytes", maxPRTriageOutputBytes)
	}
	requested, err := requestedIDSet(requestedThreadIDs)
	if err != nil {
		return nil, err
	}

	var payload struct {
		Classifications *[]PRThreadClassification `json:"classifications"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode pull-request thread triage result: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("decode pull-request thread triage result: multiple JSON values")
		}
		return nil, fmt.Errorf("decode pull-request thread triage result: %w", err)
	}
	if payload.Classifications == nil {
		return nil, errors.New("validate pull-request thread triage result: classifications are missing")
	}
	classifications := *payload.Classifications
	if len(classifications) > maxPRTriageClassifications {
		return nil, fmt.Errorf("validate pull-request thread triage result: classifications exceed %d item limit", maxPRTriageClassifications)
	}

	byID := make(map[string]PRThreadClassification, len(classifications))
	for i, classification := range classifications {
		classification.ThreadNodeID = strings.TrimSpace(classification.ThreadNodeID)
		classification.Kind = PRThreadKind(strings.TrimSpace(string(classification.Kind)))
		classification.Rationale = strings.TrimSpace(classification.Rationale)
		if classification.ThreadNodeID == "" {
			return nil, fmt.Errorf("validate pull-request thread triage result: classification %d is missing thread_node_id", i+1)
		}
		if utf8.RuneCountInString(classification.ThreadNodeID) > maxPRTriageNodeIDRunes {
			return nil, fmt.Errorf("validate pull-request thread triage result: thread_node_id %q exceeds %d runes", classification.ThreadNodeID, maxPRTriageNodeIDRunes)
		}
		if _, ok := requested[classification.ThreadNodeID]; !ok {
			return nil, fmt.Errorf("validate pull-request thread triage result: unknown thread_node_id %q", classification.ThreadNodeID)
		}
		if _, duplicate := byID[classification.ThreadNodeID]; duplicate {
			return nil, fmt.Errorf("validate pull-request thread triage result: duplicate thread_node_id %q", classification.ThreadNodeID)
		}
		if !validPRThreadKind(classification.Kind) {
			if classification.Kind == "" {
				return nil, fmt.Errorf("validate pull-request thread triage result: thread %q is missing kind", classification.ThreadNodeID)
			}
			return nil, fmt.Errorf("validate pull-request thread triage result: thread %q has unknown kind %q", classification.ThreadNodeID, classification.Kind)
		}
		if classification.Rationale == "" {
			return nil, fmt.Errorf("validate pull-request thread triage result: thread %q is missing rationale", classification.ThreadNodeID)
		}
		if utf8.RuneCountInString(classification.Rationale) > maxPRTriageRationaleRunes {
			return nil, fmt.Errorf("validate pull-request thread triage result: thread %q rationale exceeds %d runes", classification.ThreadNodeID, maxPRTriageRationaleRunes)
		}
		byID[classification.ThreadNodeID] = classification
	}

	result := make([]PRThreadClassification, 0, len(requestedThreadIDs))
	for _, requestedID := range requestedThreadIDs {
		requestedID = strings.TrimSpace(requestedID)
		classification, ok := byID[requestedID]
		if !ok {
			return nil, fmt.Errorf("validate pull-request thread triage result: missing classification for thread_node_id %q", requestedID)
		}
		result = append(result, classification)
	}
	return result, nil
}

func requestedPRThreadIDs(threads []PRThread) ([]string, error) {
	if len(threads) > maxPRTriageClassifications {
		return nil, fmt.Errorf("pull-request threads exceed %d item limit", maxPRTriageClassifications)
	}
	ids := make([]string, 0, len(threads))
	for _, thread := range threads {
		ids = append(ids, thread.NodeID)
	}
	if _, err := requestedIDSet(ids); err != nil {
		return nil, err
	}
	return ids, nil
}

func requestedIDSet(ids []string) (map[string]struct{}, error) {
	if len(ids) > maxPRTriageClassifications {
		return nil, fmt.Errorf("requested pull-request threads exceed %d item limit", maxPRTriageClassifications)
	}
	set := make(map[string]struct{}, len(ids))
	for _, rawID := range ids {
		id := strings.TrimSpace(rawID)
		if id == "" {
			return nil, errors.New("requested pull-request thread is missing node ID")
		}
		if utf8.RuneCountInString(id) > maxPRTriageNodeIDRunes {
			return nil, fmt.Errorf("requested pull-request thread node ID %q exceeds %d runes", id, maxPRTriageNodeIDRunes)
		}
		if _, duplicate := set[id]; duplicate {
			return nil, fmt.Errorf("requested pull-request thread node ID %q is duplicated", id)
		}
		set[id] = struct{}{}
	}
	return set, nil
}

func validPRThreadKind(kind PRThreadKind) bool {
	switch kind {
	case PRThreadKindChange, PRThreadKindQuestion, PRThreadKindScope, PRThreadKindUnmappable:
		return true
	default:
		return false
	}
}

func threadPacket(thread PRThread) any {
	type packetComment struct {
		CommentNodeID string `json:"comment_node_id"`
		AuthorLogin   string `json:"author_login"`
		Body          string `json:"body"`
	}
	type packetThread struct {
		ThreadNodeID string          `json:"thread_node_id"`
		Path         string          `json:"path"`
		Line         *int            `json:"line"`
		Outdated     bool            `json:"outdated"`
		Comments     []packetComment `json:"comments"`
	}
	comments := make([]packetComment, 0, len(thread.Comments))
	for _, comment := range thread.Comments {
		comments = append(comments, packetComment{CommentNodeID: comment.NodeID, AuthorLogin: comment.AuthorLogin, Body: comment.Body})
	}
	return packetThread{ThreadNodeID: thread.NodeID, Path: thread.Path, Line: thread.Line, Outdated: thread.IsOutdated, Comments: comments}
}
