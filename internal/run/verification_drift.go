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
	out.WriteString("Next: remove untracked gate outputs (they are not part of the slice) and rerun tao slice-complete. Make them ignored via .gitignore or stop the tool writing them only when that is in scope; otherwise use tao slice-blocked.")
	return out.String()
}
