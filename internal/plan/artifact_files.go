package plan

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/iamseth/tao/internal/atomicfile"
)

// planFiles is the raw artifact bundle loaded from a plan directory before deriving
// lifecycle state, summaries, or validation warnings for callers.
type planFiles struct {
	dir             string
	state           State
	slices          SlicesFile
	events          []Event
	planningSession PlanningSessionArtifacts
	planningBrief   PlanningBriefArtifact
	review          PlanReviewArtifact
	planNarrative   PlanNarrativeArtifact
	warnings        []string
}

// loadPlanFiles owns the artifact schema boundary: required JSON files are fatal,
// while optional sidecars contribute warnings so older plans remain readable.
// Journal-capable plans are read under the persistence lock. Legacy plans that
// have neither a journal nor a lock remain inspectable without creating files.
func loadPlanFiles(dir string) (planFiles, error) {
	return withMutationPersistenceReadBoundary(dir, func(recover bool) (planFiles, error) {
		if recover {
			return loadPlanFilesLocked(dir)
		}
		return readPlanFiles(dir)
	})
}

// loadPlanFilesLocked requires mutationPersistenceLock for dir.
func loadPlanFilesLocked(dir string) (planFiles, error) {
	if _, err := settlePendingMutationLocked(fileMutationJournalIO{}, dir, filepath.Base(filepath.Clean(dir))); err != nil {
		return planFiles{}, fmt.Errorf("recover plan mutation: %w", err)
	}
	return readPlanFiles(dir)
}

func readPlanFiles(dir string) (planFiles, error) {
	statePath := filepath.Join(dir, "state.json")
	slicesPath := filepath.Join(dir, "slices.json")
	eventsPath := filepath.Join(dir, "events.jsonl")

	var state State
	if err := readJSON(statePath, &state); err != nil {
		return planFiles{}, fmt.Errorf("read state.json: %w", err)
	}

	var slices SlicesFile
	if err := readJSON(slicesPath, &slices); err != nil {
		return planFiles{}, fmt.Errorf("read slices.json: %w", err)
	}

	events, warnings, err := readEvents(eventsPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return planFiles{}, fmt.Errorf("read events.jsonl: %w", err)
	}
	planningSession, artifactWarnings := readPlanningSessionArtifacts(dir)
	warnings = append(warnings, artifactWarnings...)
	planningBrief, briefWarnings := readPlanningBriefArtifact(dir)
	warnings = append(warnings, briefWarnings...)
	review, reviewWarnings := readReviewArtifact(dir)
	warnings = append(warnings, reviewWarnings...)
	planNarrative, narrativeWarnings := readPlanNarrativeArtifact(dir)
	warnings = append(warnings, narrativeWarnings...)
	warnings = append(warnings, validateRuntimePrerequisiteCycle(state.Plan.ID, state.Plan.RuntimePrerequisites, runtimePrerequisiteResolver(filepath.Dir(filepath.Clean(dir))))...)

	return planFiles{dir: dir, state: state, slices: slices, events: events, planningSession: planningSession, planningBrief: planningBrief, review: review, planNarrative: planNarrative, warnings: warnings}, nil
}

func runtimePrerequisiteResolver(plansDir string) func(string) ([]RuntimePrerequisite, bool) {
	return func(planID string) ([]RuntimePrerequisite, bool) {
		if planID == "" || len(planID) > maxRuntimePrerequisitePlanID || planID == "." || planID == ".." || strings.ContainsAny(planID, `/\\`) {
			return nil, false
		}
		var state State
		if err := readJSON(filepath.Join(plansDir, planID, "state.json"), &state); err != nil || state.Plan.ID != planID {
			return nil, false
		}
		return state.Plan.RuntimePrerequisites, true
	}
}

// ReadState reads the mutable state.json artifact from a plan directory after
// settling any durable mutation intent. Legacy plans without recovery metadata
// remain readable without creating a persistence lock file.
func ReadState(planDir string) (State, error) {
	return withMutationPersistenceReadBoundary(planDir, func(recover bool) (State, error) {
		if recover {
			if _, err := settlePendingMutationLocked(fileMutationJournalIO{}, planDir, filepath.Base(filepath.Clean(planDir))); err != nil {
				return State{}, fmt.Errorf("recover plan mutation: %w", err)
			}
		}
		var state State
		if err := readJSON(filepath.Join(planDir, "state.json"), &state); err != nil {
			return state, fmt.Errorf("read state.json: %w", err)
		}
		return state, nil
	})
}

// writeState writes the mutable state.json artifact while coordinating with
// journal recovery. PlanRecord callers use the rebasing helpers below; this
// lower-level writer is retained for artifact creation and test support.
func writeState(planDir string, state State) error {
	_, err := withMutationPersistenceLock(planDir, func() (struct{}, error) {
		if _, settleErr := settlePendingMutationLocked(fileMutationJournalIO{}, planDir, state.Plan.ID); settleErr != nil {
			return struct{}{}, fmt.Errorf("recover plan mutation: %w", settleErr)
		}
		if writeErr := writeJSON(filepath.Join(planDir, "state.json"), state); writeErr != nil {
			return struct{}{}, fmt.Errorf("write state.json: %w", writeErr)
		}
		return struct{}{}, nil
	})
	if err != nil {
		return fmt.Errorf("write state.json: %w", err)
	}
	return nil
}

// writeSlices writes the mutable slices.json artifact while coordinating with
// journal recovery. PlanRecord callers use the rebasing helpers below.
func writeSlices(planDir string, slices SlicesFile) error {
	_, err := withMutationPersistenceLock(planDir, func() (struct{}, error) {
		if _, settleErr := settlePendingMutationLocked(fileMutationJournalIO{}, planDir, slices.PlanID); settleErr != nil {
			return struct{}{}, fmt.Errorf("recover plan mutation: %w", settleErr)
		}
		if writeErr := writeJSON(filepath.Join(planDir, "slices.json"), slices); writeErr != nil {
			return struct{}{}, fmt.Errorf("write slices.json: %w", writeErr)
		}
		return struct{}{}, nil
	})
	if err != nil {
		return fmt.Errorf("write slices.json: %w", err)
	}
	return nil
}

// AppendEvent appends one lifecycle event to events.jsonl in a plan directory.
func AppendEvent(planDir string, event Event) error {
	file, err := os.OpenFile(filepath.Join(planDir, "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- plan artifacts are local files selected by the user/configured plans directory.
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		_ = file.Close()
		return err
	}
	if _, err := fmt.Fprintln(file, string(encoded)); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// writeJSON encodes value as JSON and deep-merges it over any existing file at
// path before writing atomically. The merge preserves unknown fields from older
// plan artifacts. PlanRecord writers carry explicit clear-or-replace intent in
// artifactChangeSet; migrated omitempty groups emit explicit replacement or
// empty values only when that intent is declared. Remaining clearable fields
// retain the tag-driven contract. The
// low-level creation/test writer remains preserve-free and emits exactly what
// the current struct tags encode.
func writeJSON(path string, value any) error {
	encoded, err := prepareJSON(path, value, artifactJSONChanges{})
	if err != nil {
		return err
	}
	return atomicWriteFile(path, encoded)
}

// prepareJSON returns the exact final bytes for a merge-preserving artifact
// write. Typed clear-or-replace intent is lowered before the unknown-field
// preserving merge. Callers may durably journal these bytes before installing
// the same byte slice with atomicWriteFile.
func prepareJSON(path string, value any, changes artifactJSONChanges) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	encoded, err = lowerArtifactJSONChanges(encoded, changes)
	if err != nil {
		return nil, err
	}
	if existing, err := os.ReadFile(path); err == nil { //nolint:gosec // G304: path is internally constructed plan artifact path
		if merged, err := mergeJSON(existing, encoded); err == nil {
			encoded = merged
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	encoded, err = removeOmittedArtifactJSONFields(encoded, changes)
	if err != nil {
		return nil, err
	}
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, encoded, "", "  "); err != nil {
		return nil, err
	}
	return append(formatted.Bytes(), '\n'), nil
}

func lowerArtifactJSONChanges(encoded []byte, projection artifactJSONChanges) ([]byte, error) {
	changes := projection.changes
	if changes == nil {
		return encoded, nil
	}
	hasStateChanges := changes.clearWorkspaceDependencyFailure || changes.clearWorkspaceDependencyFingerprint || changes.clearWorkspaceRebaseIntent || changes.clearPlanCurrentSlice || changes.clearPlanFinalizationFailure || changes.clearSingleMergeResolution || changes.planFinalizationFailure != nil || changes.finalVerification != nil || changes.planReview.kind != planReviewUnchanged
	hasSliceChanges := len(changes.clearSliceBlockerNotes) > 0 || len(changes.clearSliceExecutionBoundaries) > 0
	if projection.kind == artifactJSONState && !hasStateChanges || projection.kind == artifactJSONSlices && !hasSliceChanges || projection.kind == artifactJSONNone {
		return encoded, nil
	}

	var root map[string]any
	if err := json.Unmarshal(encoded, &root); err != nil {
		return nil, fmt.Errorf("lower artifact changes: %w", err)
	}
	switch projection.kind {
	case artifactJSONState:
		if err := lowerStateJSONChanges(root, changes); err != nil {
			return nil, err
		}
	case artifactJSONSlices:
		if err := lowerSlicesJSONChanges(root, changes); err != nil {
			return nil, err
		}
	}
	lowered, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("lower artifact changes: %w", err)
	}
	return lowered, nil
}

func removeOmittedArtifactJSONFields(encoded []byte, projection artifactJSONChanges) ([]byte, error) {
	changes := projection.changes
	if projection.kind != artifactJSONState || changes == nil || changes.finalVerification == nil || changes.finalVerification.FailureKind != "" && changes.finalVerification.ExitCode != nil {
		return encoded, nil
	}
	var root map[string]any
	if err := json.Unmarshal(encoded, &root); err != nil {
		return nil, fmt.Errorf("remove omitted artifact fields: %w", err)
	}
	plan, _ := root["plan"].(map[string]any)
	verification, _ := plan["final_verification"].(map[string]any)
	if changes.finalVerification.FailureKind == "" {
		delete(verification, "failure_kind")
	}
	if changes.finalVerification.ExitCode == nil {
		delete(verification, "exit_code")
	}
	cleaned, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("remove omitted artifact fields: %w", err)
	}
	return cleaned, nil
}

func atomicWriteFile(path string, data []byte) error {
	return atomicfile.Write(path, data, atomicfile.Options{})
}

func isUnsupportedDirSyncError(err error) bool {
	return errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.ENOSYS)
}

// mergeJSON deep-merges update over existing: objects are merged key-by-key,
// arrays are merged by their id or plan_id field (or fully replaced when no
// identity is present or when update is empty), and all other values are
// replaced by the update.
// See writeJSON for the clearable-field contract that this function enforces.
func mergeJSON(existing []byte, update []byte) ([]byte, error) {
	var existingValue any
	if err := json.Unmarshal(existing, &existingValue); err != nil {
		return nil, err
	}
	var updateValue any
	if err := json.Unmarshal(update, &updateValue); err != nil {
		return nil, err
	}
	return json.Marshal(mergeJSONValue(existingValue, updateValue))
}

func mergeJSONValue(existing any, update any) any {
	existingObject, existingIsObject := existing.(map[string]any)
	updateObject, updateIsObject := update.(map[string]any)
	if existingIsObject && updateIsObject {
		for key, value := range updateObject {
			existingObject[key] = mergeJSONValue(existingObject[key], value)
		}
		return existingObject
	}
	existingArray, existingIsArray := existing.([]any)
	updateArray, updateIsArray := update.([]any)
	if existingIsArray && updateIsArray {
		return mergeJSONArray(existingArray, updateArray)
	}
	return update
}

func mergeJSONArray(existing []any, update []any) []any {
	existingByID := make(map[objectIdentity]any, len(existing))
	for _, value := range existing {
		for _, identity := range objectIdentities(value) {
			existingByID[identity] = value
		}
	}
	merged := make([]any, 0, len(update))
	for _, value := range update {
		if identity, ok := objectID(value); ok {
			if existingValue, found := existingByID[identity]; found {
				value = mergeJSONValue(existingValue, value)
			}
		}
		merged = append(merged, value)
	}
	return merged
}

type objectIdentity struct {
	field string
	value string
}

func objectIdentities(value any) []objectIdentity {
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	identities := make([]objectIdentity, 0, 2)
	for _, field := range []string{"id", "plan_id"} {
		if id, _ := object[field].(string); id != "" {
			identities = append(identities, objectIdentity{field: field, value: id})
		}
	}
	return identities
}

func objectID(value any) (objectIdentity, bool) {
	identities := objectIdentities(value)
	if len(identities) == 0 {
		return objectIdentity{}, false
	}
	return identities[0], true
}

func readJSON(path string, out any) error {
	file, err := os.Open(path) // #nosec G304 -- plan artifacts are local files selected by the user/configured plans directory.
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	decoder := json.NewDecoder(file)
	if err := decoder.Decode(out); err != nil {
		return err
	}
	return nil
}

const maxEventJSONLLineBytes = 1024 * 1024

func readEvents(path string) ([]Event, []string, error) {
	file, err := os.Open(path) // #nosec G304 -- plan artifacts are local files selected by the user/configured plans directory.
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = file.Close() }()

	var events []Event
	var warnings []string
	reader := bufio.NewReader(file)
	line := 0
	for {
		lineBytes, oversized, err := readLimitedJSONLLine(reader, maxEventJSONLLineBytes)
		if errors.Is(err, io.EOF) && len(lineBytes) == 0 && !oversized {
			break
		}
		line++
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, warnings, err
		}
		if oversized {
			warnings = append(warnings, fmt.Sprintf("events.jsonl line %d exceeds %d bytes; skipped", line, maxEventJSONLLineBytes))
			if errors.Is(err, io.EOF) {
				break
			}
			continue
		}
		text := strings.TrimSpace(string(lineBytes))
		if text != "" {
			var event Event
			if err := json.Unmarshal([]byte(text), &event); err != nil {
				warnings = append(warnings, fmt.Sprintf("events.jsonl line %d: %v", line, err))
			} else {
				events = append(events, event)
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}
	return events, warnings, nil
}

func readLimitedJSONLLine(reader *bufio.Reader, maxBytes int) ([]byte, bool, error) {
	var line []byte
	oversized := false
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(fragment) > 0 && !oversized {
			if len(line)+len(fragment) > maxBytes {
				oversized = true
				line = nil
			} else {
				line = append(line, fragment...)
			}
		}
		switch {
		case err == nil:
			return line, oversized, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if len(fragment) == 0 && len(line) == 0 && !oversized {
				return nil, false, io.EOF
			}
			return line, oversized, io.EOF
		default:
			return line, oversized, err
		}
	}
}

// Optional sidecar readers below keep local-only planning artifacts out of the
// core state/slices schema and surface unreadable data as warnings.
func readPlanningSessionArtifacts(dir string) (PlanningSessionArtifacts, []string) {
	artifacts := PlanningSessionArtifacts{}
	var warnings []string

	exportPath := filepath.Join(dir, PlanningSessionExportFile)
	if info, err := os.Stat(exportPath); err == nil && !info.IsDir() {
		artifacts.ExportPath = exportPath
		artifacts.HasExport = true
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		warnings = append(warnings, fmt.Sprintf("%s: %v", PlanningSessionExportFile, err))
	}

	statsPath := filepath.Join(dir, PlanningSessionStatsFile)
	var stats PlanningSessionStats
	if ok, err := readOptionalJSON(statsPath, &stats); err != nil {
		warnings = append(warnings, fmt.Sprintf("%s: %v", PlanningSessionStatsFile, err))
	} else if ok {
		artifacts.Stats = &stats
		if !stats.PromptExtracted && stats.PromptExtractionNote != "" {
			warnings = append(warnings, fmt.Sprintf("%s: planning prompt extraction failed: %s", PlanningSessionStatsFile, stats.PromptExtractionNote))
		}
	}

	promptPath := filepath.Join(dir, PlanningPromptFile)
	if content, err := os.ReadFile(promptPath); err == nil { //nolint:gosec // G304: promptPath is internally constructed plan artifact path
		artifacts.PromptPath = promptPath
		artifacts.Prompt = string(content)
	} else if !errors.Is(err, os.ErrNotExist) {
		warnings = append(warnings, fmt.Sprintf("%s: %v", PlanningPromptFile, err))
	}

	return artifacts, warnings
}

func readPlanningBriefArtifact(dir string) (PlanningBriefArtifact, []string) {
	path := filepath.Join(dir, PlanningBriefFile)
	content, err := os.ReadFile(path) // #nosec G304 -- optional plan artifacts are local files selected by the user/configured plans directory.
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return PlanningBriefArtifact{}, nil
		}
		return PlanningBriefArtifact{}, []string{fmt.Sprintf("%s: %v", PlanningBriefFile, err)}
	}
	return PlanningBriefArtifact{Path: path, Content: string(content)}, nil
}

func readReviewArtifact(dir string) (PlanReviewArtifact, []string) {
	path := filepath.Join(dir, ReviewFile)
	content, err := os.ReadFile(path) // #nosec G304 -- optional plan artifacts are local files selected by the user/configured plans directory.
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return PlanReviewArtifact{}, nil
		}
		return PlanReviewArtifact{}, []string{fmt.Sprintf("%s: %v", ReviewFile, err)}
	}
	return PlanReviewArtifact{Path: path, Content: string(content)}, nil
}

func readPlanNarrativeArtifact(dir string) (PlanNarrativeArtifact, []string) {
	path := filepath.Join(dir, PlanMarkdownFile)
	content, err := os.ReadFile(path) // #nosec G304 -- optional plan artifacts are local files selected by the user/configured plans directory.
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return PlanNarrativeArtifact{}, nil
		}
		return PlanNarrativeArtifact{}, []string{fmt.Sprintf("%s: %v", PlanMarkdownFile, err)}
	}
	return PlanNarrativeArtifact{Path: path, Content: string(content)}, nil
}

func readOptionalJSON(path string, out any) (bool, error) {
	file, err := os.Open(path) // #nosec G304 -- optional plan artifacts are local files selected by the user/configured plans directory.
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer func() { _ = file.Close() }()

	decoder := json.NewDecoder(file)
	if err := decoder.Decode(out); err != nil {
		return true, err
	}
	return true, nil
}
