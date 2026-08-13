package verification

import (
	"reflect"
	"testing"
)

func TestCommandTokenProjections(t *testing.T) {
	tests := []struct {
		name      string
		command   string
		fields    []string
		pipelines [][]string
	}{
		{
			name:      "spaced separators",
			command:   "go test ./... && make lint ; echo done || exit 1 | tee log & wait",
			fields:    []string{"go", "test", "./...", "&&", "make", "lint", ";", "echo", "done", "||", "exit", "1", "|", "tee", "log", "&", "wait"},
			pipelines: [][]string{{"go", "test", "./..."}, {"make", "lint"}, {"echo", "done"}, {"exit", "1"}, {"tee", "log"}, {"wait"}},
		},
		{
			name:      "compact separators",
			command:   "cd pkg&&go test;make lint||exit 1|tee log&wait",
			fields:    []string{"cd", "pkg", "&&", "go", "test", ";", "make", "lint", "||", "exit", "1", "|", "tee", "log", "&", "wait"},
			pipelines: [][]string{{"cd", "pkg"}, {"go", "test"}, {"make", "lint"}, {"exit", "1"}, {"tee", "log"}, {"wait"}},
		},
		{
			name:      "quoted separators",
			command:   `printf '%s|%s;still' "a&&b" 'c&d' pre'|'post`,
			fields:    []string{"printf", "%s|%s;still", "a&&b", "c&d", "pre|post"},
			pipelines: [][]string{{"printf", "%s|%s;still", "a&&b", "c&d", "pre|post"}},
		},
		{
			name:      "escaped separators",
			command:   `printf a\|b c\;d e\&f 'g\|h'`,
			fields:    []string{"printf", "a|b", "c;d", "e&f", `g\|h`},
			pipelines: [][]string{{"printf", "a|b", "c;d", "e&f", `g\|h`}},
		},
		{
			name:      "whitespace and newlines",
			command:   " \tgo\rtest\n\nmake lint \t",
			fields:    []string{"go", "test", "\n", "\n", "make", "lint"},
			pipelines: [][]string{{"go", "test"}, {"make", "lint"}},
		},
		{
			name:      "single quoted backslashes",
			command:   `echo 'C:\tmp\file'`,
			fields:    []string{"echo", `C:\tmp\file`},
			pipelines: [][]string{{"echo", `C:\tmp\file`}},
		},
		{
			name:      "trailing escape",
			command:   `echo trail\`,
			fields:    []string{"echo", `trail\`},
			pipelines: [][]string{{"echo", `trail\`}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CommandFields(tt.command); !reflect.DeepEqual(got, tt.fields) {
				t.Errorf("CommandFields(%q) = %#v, want %#v", tt.command, got, tt.fields)
			}
			if got := CommandPipelines(tt.command); !reflect.DeepEqual(got, tt.pipelines) {
				t.Errorf("CommandPipelines(%q) = %#v, want %#v", tt.command, got, tt.pipelines)
			}
		})
	}
}
