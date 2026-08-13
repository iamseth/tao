package verification

import "strings"

func shellHazardFindings(command string, tokens []string) []Finding {
	if len(tokens) < 2 || tokens[0] != "go" || tokens[1] != "test" {
		return nil
	}
	for i := 2; i < len(tokens); i++ {
		if tokens[i] == "-run" && i+1 < len(tokens) {
			pattern := tokens[i+1]
			return shellHazardFindingForRunPattern(command, runPatternWithAdjacentSeparators(command, pattern))
		}
		if after, ok := strings.CutPrefix(tokens[i], "-run="); ok {
			return shellHazardFindingForRunPattern(command, runPatternWithAdjacentSeparators(command, after))
		}
	}
	return nil
}

func runPatternWithAdjacentSeparators(command string, pattern string) string {
	tokens := splitCommandTokens(command)
	for i, token := range tokens {
		valueIndex := -1
		switch {
		case token.value == "-run" && i+1 < len(tokens) && !tokens[i+1].separator && tokens[i+1].value == pattern:
			valueIndex = i + 1
		case strings.HasPrefix(token.value, "-run=") && strings.TrimPrefix(token.value, "-run=") == pattern:
			valueIndex = i
		}
		if valueIndex < 0 {
			continue
		}

		combined := pattern
		for j := valueIndex + 1; j < len(tokens) && tokens[j].separator && tokens[j].joinedToLeft; j++ {
			combined += tokens[j].value
			if !tokens[j].joinedToRight || j+1 >= len(tokens) || tokens[j+1].separator {
				break
			}
			combined += tokens[j+1].value
			j++
		}
		if combined != pattern {
			return combined
		}
	}
	return pattern
}

func shellHazardFindingForRunPattern(command string, pattern string) []Finding {
	if pattern == "" || !containsShellSensitiveRegexMeta(pattern) || commandValueQuoted(command, pattern) {
		return nil
	}
	return []Finding{{
		Severity:   FindingWarning,
		Code:       "verification_shell_hazard",
		Message:    "verification command uses an unquoted go test -run pattern with shell-sensitive regex metacharacters",
		Command:    command,
		Suggestion: quoteShellArg(pattern),
	}}
}

func containsShellSensitiveRegexMeta(value string) bool {
	return strings.ContainsAny(value, "*?[](){}|&;<>$`")
}

func commandValueQuoted(command string, value string) bool {
	for _, token := range splitCommandTokens(command) {
		if token.value == value && token.quoted {
			return true
		}
		if strings.HasPrefix(token.value, "-run=") && strings.TrimPrefix(token.value, "-run=") == value && token.quoted {
			return true
		}
	}
	return false
}

func quoteShellArg(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
