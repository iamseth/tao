package verification

import "strings"

// CommandFields returns shell-like command fields, including unquoted shell
// separator spellings as fields.
func CommandFields(command string) []string {
	return splitCommandFields(command)
}

// CommandPipelines returns non-empty field groups separated by unquoted shell
// command separators.
func CommandPipelines(command string) [][]string {
	tokens := splitCommandTokens(command)
	pipelines := make([][]string, 0)
	fields := make([]string, 0)
	for _, token := range tokens {
		if token.separator {
			if len(fields) > 0 {
				pipelines = append(pipelines, fields)
				fields = nil
			}
			continue
		}
		fields = append(fields, token.value)
	}
	if len(fields) > 0 {
		pipelines = append(pipelines, fields)
	}
	return pipelines
}

func splitCommandFields(command string) []string {
	tokens := splitCommandTokens(command)
	fields := make([]string, 0, len(tokens))
	for _, token := range tokens {
		fields = append(fields, token.value)
	}
	return fields
}

type commandToken struct {
	value         string
	quoted        bool
	separator     bool
	joinedToLeft  bool
	joinedToRight bool
}

func splitCommandTokens(command string) []commandToken {
	tokens := make([]commandToken, 0)
	var current strings.Builder
	var quote rune
	quoted := false
	escaped := false
	flush := func() {
		if current.Len() == 0 {
			return
		}
		tokens = append(tokens, commandToken{value: current.String(), quoted: quoted})
		current.Reset()
		quoted = false
	}
	separator := func(value string, joinedToRight bool) {
		joinedToLeft := current.Len() > 0
		flush()
		tokens = append(tokens, commandToken{
			value:         value,
			separator:     true,
			joinedToLeft:  joinedToLeft,
			joinedToRight: joinedToRight,
		})
	}

	runes := []rune(command)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if quote == '\'' {
			if r == quote {
				quote = 0
			} else {
				current.WriteRune(r)
			}
			continue
		}
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		if quote == '"' {
			switch r {
			case quote:
				quote = 0
			case '\\':
				escaped = true
			default:
				current.WriteRune(r)
			}
			continue
		}

		switch r {
		case '\\':
			escaped = true
		case '\'', '"':
			quote = r
			quoted = true
		case ' ', '\t', '\r':
			flush()
		case '\n', ';':
			separator(string(r), i+1 < len(runes) && !isCommandSpace(runes[i+1]))
		case '&', '|':
			value := string(r)
			if i+1 < len(runes) && runes[i+1] == r {
				value += string(r)
				i++
			}
			separator(value, i+1 < len(runes) && !isCommandSpace(runes[i+1]))
		default:
			current.WriteRune(r)
		}
	}
	if escaped {
		current.WriteRune('\\')
	}
	flush()
	return tokens
}

func isCommandSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\r' || r == '\n'
}
