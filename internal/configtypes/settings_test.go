package configtypes

import (
	"encoding/json"
	"testing"
)

func TestSettingsValuesClone(t *testing.T) {
	var absent SettingsValues
	if absent.Clone() != nil {
		t.Fatal("nil layer changed")
	}
	original := SettingsValues{"pull_request": json.RawMessage("false"), "budget.slice.cost.stop": json.RawMessage("null")}
	clone := original.Clone()
	clone["pull_request"][0] = 't'
	delete(clone, "budget.slice.cost.stop")
	if string(original["pull_request"]) != "false" || string(original["budget.slice.cost.stop"]) != "null" {
		t.Fatal("clone aliases original")
	}
}
