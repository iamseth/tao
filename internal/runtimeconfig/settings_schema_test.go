package runtimeconfig

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/configtypes"
)

func TestSettingsRoundTrip(t *testing.T) {
	input := []byte(`{"pull_request":false,"max_slices":0,"models":{"model":"provider/model"},"session_timeout":"2m","budget":{"slice":{"cost":{"stop":null,"warn":10}}}}`)
	values, err := DecodeSettings(input, "repo")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeSettings(values, "repo")
	if err != nil {
		t.Fatal(err)
	}
	again, err := DecodeSettings(encoded, "repo")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(values, again) {
		t.Fatalf("round trip: %s", encoded)
	}
	if string(values["pull_request"]) != "false" || string(values["budget.slice.cost.stop"]) != "null" {
		t.Fatal(values)
	}
	delete(values, "budget.slice.cost.stop")
	if err := ValidateSettings(values, "repo"); err != nil {
		t.Fatal(err)
	}
}

func TestSettingsRejectMalformed(t *testing.T) {
	for _, input := range []string{`null`, `[]`, `{} {}`, `{"pull_request":false,"pull_request":true}`, `{"models":{"model":"a","model":"b"}}`, `{"models.model":"a"}`, `{"models":null}`, `{"models":[]}`, `{"unknown":1}`, `{"pull_request":null}`, `{"max_slices":"1"}`, `{"max_slices":1.5}`, `{"pull_request":1}`, `{"budget":{"plan":{"cost":{"stop":1}}}}`, `{"models":{"unknown":"a"}}`, `{"session_timeout":1}`} {
		t.Run(input, func(t *testing.T) {
			if _, err := DecodeSettings([]byte(input), "repo"); err == nil {
				t.Fatal("accepted invalid object")
			}
		})
	}
	if _, err := DecodeSettings([]byte(`{"theme":"default"}`), "repo"); err == nil {
		t.Fatal("repo theme accepted")
	}
	if err := ValidateSettings(nil, "unknown"); err == nil {
		t.Fatal("unknown scope accepted")
	}
	if _, err := EncodeSettings(configtypes.SettingsValues{"pull_request": json.RawMessage(`"false"`)}, "repo"); err == nil {
		t.Fatal("string boolean accepted")
	}
}

func TestSettingsParsers(t *testing.T) {
	for _, tc := range []struct{ key, input, want string }{
		{"pull_request", "OFF", "false"}, {"max_slices", "0", "0"}, {"session_timeout", "120s", `"2m0s"`}, {"models.run_model", " x/y ", `"x/y"`}, {"budget.slice.cost.stop", "null", "null"}, {"budget.slice.output_tokens.stop", "0", "0"}, {"budget.plan.cost.warn", "1.25", "1.25"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			got, err := ParseSetting(tc.key, tc.input)
			if err != nil || string(got) != tc.want {
				t.Fatalf("%s, %v", got, err)
			}
		})
	}
	for _, tc := range []struct{ key, input string }{{"max_slices", "-1"}, {"max_slices", "null"}, {"session_timeout", "-1s"}, {"session_timeout", "forever"}, {"session_warn_percent", "100"}, {"max_rework_attempts", "-1"}, {"rework_escalation_from_attempt", "0"}, {"agent", "other"}, {"commit_policy", "bad"}, {"execution_mode", "bad"}, {"models.model", "a\nb"}, {"budget.slice.cost.stop", "NaN"}, {"budget.slice.cost.warn", "null"}, {"budget.plan.tool_calls.warn", "-1"}, {"budget.plan.output_tokens.warn", "1.5"}, {"pull_request", "maybe"}, {"unknown", "1"}, {"approved_by", "a"}, {"theme", "bad"}, {"update", "bad"}} {
		t.Run(tc.key+tc.input, func(t *testing.T) {
			if _, err := ParseSetting(tc.key, tc.input); err == nil {
				t.Fatal("accepted invalid value")
			}
		})
	}
}

func TestSettingsParserParity(t *testing.T) {
	inputs := []string{"", " ", "true", "false", "YES", "off", "0", "1", "-1", "99", "100", "1.5", "1e3", "9999999999999999999999999", "NaN", "+Inf", "null", "slice", "none", "plan", "isolated", "current", "pi", "claude", "20m", "-1s", "provider/model", "a\nb", "warn", "off", "auto", "default"}
	for _, row := range runtimeEnvVars {
		if row.aliasOf != "" || len(row.setting.Scopes) == 0 {
			continue
		}
		t.Run(row.name, func(t *testing.T) {
			for _, input := range inputs {
				expected, envErr := row.apply(&EnvDefaults{}, input)
				got, err := ParseSetting(row.setting.Key, input)
				if strings.HasSuffix(row.setting.Key, ".stop") && input == "null" {
					if err != nil || string(got) != "null" {
						t.Fatal("STOP null rejected")
					}
					continue
				}
				if (envErr == nil) != (err == nil) {
					t.Fatalf("%q: env=%v setting=%v", input, envErr, err)
				}
				if err != nil {
					continue
				}
				if strings.HasSuffix(row.setting.Key, ".stop") && expected == "disabled" {
					expected = "null"
				}
				if row.setting.Kind == "string" {
					encoded, _ := json.Marshal(expected)
					expected = string(encoded)
				}
				if string(got) != expected {
					t.Fatalf("%q: got %s want %s", input, got, expected)
				}
				if err := ValidateSettings(configtypes.SettingsValues{row.setting.Key: got}, "global"); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestSettingInventory(t *testing.T) {
	defs := SettingDefinitions()
	seen := map[string]bool{}
	expected := strings.Fields("max_slices commit_policy execution_mode agent pull_request review_enabled session_timeout session_warn_percent review_agent max_rework_attempts rework_escalation_from_attempt dangerously_skip_permissions run_header models.model models.run_model models.review_model models.merge_review_model models.resolver_model models.rework_escalation_model theme update budget.slice.output_tokens.warn budget.slice.output_tokens.stop budget.slice.cost.warn budget.slice.cost.stop budget.slice.tool_calls.warn budget.slice.assistant_messages.warn budget.slice.errored_messages.warn budget.plan.output_tokens.warn budget.plan.cost.warn budget.plan.tool_calls.warn budget.plan.assistant_messages.warn budget.plan.errored_messages.warn")
	for _, key := range expected {
		d, err := settingDefinition(key)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"global", "repo"}
		if key == "theme" || key == "update" {
			want = []string{"global"}
		}
		if !reflect.DeepEqual(d.Scopes, want) {
			t.Fatalf("%s scopes %v", key, d.Scopes)
		}
	}
	persistable, budgets := 0, 0
	for _, d := range defs {
		if seen[d.Key] {
			t.Fatalf("duplicate %s", d.Key)
		}
		seen[d.Key] = true
		if len(d.Scopes) == 0 {
			continue
		}
		persistable++
		if len(d.Key) > 7 && d.Key[:7] == "budget." {
			budgets++
		}
		if d.EnvKey == "" {
			if d.Key != "max_slices" {
				t.Fatal(d)
			}
			continue
		}
		var row *runtimeEnvVar
		for i := range runtimeEnvVars {
			if runtimeEnvVars[i].name == d.EnvKey {
				row = &runtimeEnvVars[i]
				break
			}
		}
		if row == nil {
			t.Fatal(d)
		}
		input := row.defaultValue(DefaultRunOptionsPatch())
		if len(d.Key) > 7 && d.Key[:7] == "models." {
			input = "provider/model"
		}
		if input == "disabled" {
			input = "null"
		}
		value, err := ParseSetting(d.Key, input)
		if err != nil {
			t.Fatalf("%s: %v", d.Key, err)
		}
		if err := ValidateSettings(configtypes.SettingsValues{d.Key: value}, "global"); err != nil {
			t.Fatal(err)
		}
	}
	if persistable != 33 || budgets != 12 {
		t.Fatalf("persistable=%d budgets=%d", persistable, budgets)
	}
	for _, row := range runtimeEnvVars {
		if row.aliasOf != "" {
			continue
		}
		found := false
		for _, d := range defs {
			if d.EnvKey == row.name {
				found = true
			}
		}
		if !found {
			t.Fatal(row.name)
		}
	}
	for i := range defs {
		if len(defs[i].Scopes) > 0 {
			defs[i].Scopes[0] = "bad"
		}
		if len(defs[i].Choices) > 0 {
			defs[i].Choices[0] = "bad"
		}
	}
	for _, d := range SettingDefinitions() {
		for _, s := range d.Scopes {
			if s == "bad" {
				t.Fatal("shared scopes")
			}
		}
		for _, c := range d.Choices {
			if c == "bad" {
				t.Fatal("shared choices")
			}
		}
	}
}
