package verification

import "strings"

// CommandFields returns shell-like command fields without exposing tokenization details.
func CommandFields(command string) []string {
	return splitCommandFields(command)
}

func splitCommandFields(command string) []string {
	tokens := splitCommandTokens(command)
	fields := make([]string, 0)
	for _, token := range tokens {
		fields = append(fields, token.value)
	}
	return fields
}

type commandToken struct {
	value  string
	quoted bool
}

func splitCommandTokens(command string) []commandToken {
	tokens := make([]commandToken, 0)
	var current strings.Builder
	var quote rune
	quoted := false
	escaped := false
	for _, r := range command {
		switch {
		case escaped:
			current.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				current.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
			quoted = true
		case r == ' ' || r == '\t' || r == '\n':
			if current.Len() > 0 {
				tokens = append(tokens, commandToken{value: current.String(), quoted: quoted})
				current.Reset()
				quoted = false
			}
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		tokens = append(tokens, commandToken{value: current.String(), quoted: quoted})
	}
	return tokens
}
