package runtimeconfig

import (
	"encoding/json"
	"maps"

	"github.com/iamseth/tao/internal/gateenv"
)

func applyGateEnv(d *EnvDefaults, value string) (string, error) {
	pins, err := gateenv.Parse([]byte(value))
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(pins)
	if err != nil {
		return "", err
	}
	// Canonical JSON may be larger because encoding escapes control characters.
	if _, err := gateenv.Parse(encoded); err != nil {
		return "", err
	}
	d.GateEnv = pins
	return string(encoded), nil
}

// GateEnv returns independent repository pins, never the process environment.
func (s EnvSnapshot) GateEnv() (map[string]string, error) {
	if err := s.Require("gate_env"); err != nil {
		return nil, err
	}
	return maps.Clone(s.defaults.GateEnv), nil
}
