package gateenv

import (
	"reflect"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	for _, input := range []string{`null`, `[]`, `{"CI":null}`, `{"CI":1}`, `{"CI":{}}`, `{"CI":true}`, `{"CI":[]}`, `{"PATH":"x"}`, `{"CI":"a","CI":"b"}`, `{} {}`, `{`, `{"CI":"\u0000"}`, `{"CI":"` + strings.Repeat("x", 4097) + `"}`, strings.Repeat(" ", 16385) + `{}`} {
		if got, err := Parse([]byte(input)); err == nil || got != nil {
			t.Errorf("accepted invalid input %.80q: %v", input, got)
		}
	}
	want := map[string]string{"CI": "", "GOFLAGS": "$(echo nope)", "GOTOOLCHAIN": strings.Repeat("x", 4096)}
	got, err := Parse([]byte(`{"CI":"","GOFLAGS":"$(echo nope)","GOTOOLCHAIN":"` + want["GOTOOLCHAIN"] + `"}`))
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("parse: %v %v", got, err)
	}
	if got, err := Parse([]byte(`{}`)); err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
	if _, err := Parse([]byte(`{}` + strings.Repeat(" ", 16382))); err != nil {
		t.Fatal(err)
	}
}

func TestApply(t *testing.T) {
	input := []string{"CI=old", "OTHER=a=b", "CI=duplicate", "GOFLAGS=old"}
	pins := map[string]string{"CI": "", "GOFLAGS": "$literal"}
	got, err := Apply(input, pins)
	want := []string{"OTHER=a=b", "CI=", "GOFLAGS=$literal"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal(got, err)
	}
	got[0] = "changed"
	if input[0] != "CI=old" || pins["GOFLAGS"] != "$literal" {
		t.Fatal("aliased inputs")
	}
	copyEnv, err := Apply(input, nil)
	if err != nil {
		t.Fatal(err)
	}
	copyEnv[0] = "changed"
	if input[0] != "CI=old" {
		t.Fatal("nil pins aliases input")
	}
	for _, pins := range []map[string]string{{"PATH": "x"}, {"CI": "\x00"}, {"CI": strings.Repeat("x", 4097)}} {
		if _, err := Apply(input, pins); err == nil {
			t.Fatal("accepted invalid pins")
		}
	}
}
