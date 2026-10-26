package run

import (
	"fmt"
	"slices"
	"strings"
)

func describeVerificationBoundaryDrift(before, after verifiedCompletionInspection) string {
	var out strings.Builder
	out.WriteString("slice verification boundary changed after gates passed; no intent recorded\n")
	for _, field := range []struct{ name, before, after string }{
		{"execution root", before.root, after.root},
		{"branch", before.branch, after.branch},
		{"head", before.head, after.head},
		{"index fingerprint", before.indexFingerprint, after.indexFingerprint},
		{"declaration digest", before.declaration, after.declaration},
		{"policy", before.policy, after.policy},
		{"strategy", before.strategy, after.strategy},
	} {
		if field.before != field.after {
			fmt.Fprintf(&out, "%s changed\n", field.name)
		}
	}
	var trackedDrift, untrackedDrift bool
	if before.fingerprint != after.fingerprint {
		out.WriteString("worktree fingerprint changed\n")
		var appeared, changed, vanished []string
		for path, record := range after.paths {
			prior, existed := before.paths[path]
			switch {
			case existed && prior == record:
				continue
			case record == "deleted":
				vanished = append(vanished, path)
			case !existed || prior == "deleted":
				appeared = append(appeared, path)
			default:
				changed = append(changed, path)
			}
		}
		for path := range before.paths {
			if _, exists := after.paths[path]; !exists {
				vanished = append(vanished, path)
			}
		}
		for _, category := range []struct {
			name  string
			paths []string
		}{
			{"appeared", appeared}, {"changed", changed}, {"vanished", vanished},
		} {
			if len(category.paths) == 0 {
				continue
			}
			for _, path := range category.paths {
				if before.untracked[path] || after.untracked[path] {
					untrackedDrift = true
				} else {
					trackedDrift = true
				}
			}
			fmt.Fprintf(&out, "%s:\n", category.name)
			slices.Sort(category.paths)
			for _, path := range category.paths[:min(20, len(category.paths))] {
				marker := ""
				if before.untracked[path] || after.untracked[path] {
					marker = " (untracked)"
				}
				// The shared stream sanitizer preserves newlines and tabs; escape
				// those too so a filename cannot forge another listing entry.
				presented := verificationPresentation(path, 512)
				presented = strings.NewReplacer("\n", `\n`, "\t", `\t`).Replace(presented)
				fmt.Fprintf(&out, "  %s%s\n", presented, marker)
			}
			if extra := len(category.paths) - 20; extra > 0 {
				fmt.Fprintf(&out, "  ... and %d more\n", extra)
			}
		}
	}
	out.WriteString("Next: inspect the changes before retrying tao slice-complete; timing alone does not identify which command or process changed the boundary. ")
	if trackedDrift {
		out.WriteString("tracked files changed during verification; preserve intentional edits. ")
	}
	if untrackedDrift {
		out.WriteString("Untracked paths changed during verification; inspect ownership and contents before removing any disposable outputs. Ignoring outputs or stopping their creation is appropriate only when in scope. ")
	}
	if trackedDrift || untrackedDrift {
		out.WriteString("For known verification side effects, use a documented repository-owned safe verification entrypoint that handles them inside the declared command, preserves intentional edits and failure exit status, and supports repeated invocation. Agent pre-call cleanup cannot prevent mutation during internal gates. This diagnostic does not authorize cleanup or changes to plan artifacts or verification declarations. ")
	}
	out.WriteString("Resolve any reported identity, index, or declaration changes separately; a safe entrypoint does not bypass boundary checks. If no safe in-scope path exists, use tao slice-blocked.")
	return out.String()
}
