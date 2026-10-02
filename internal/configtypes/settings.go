package configtypes

import "encoding/json"

// SettingsValues is a sparse layer of native JSON scalars indexed by canonical
// dotted keys. Absence means inherit; null is reserved for disabled STOP caps.
// Owners must Clone when accepting or exposing a layer across an API boundary.
type SettingsValues map[string]json.RawMessage

// Clone returns an independent layer, including the underlying JSON bytes.
func (v SettingsValues) Clone() SettingsValues {
	if v == nil {
		return nil
	}
	out := make(SettingsValues, len(v))
	for key, value := range v {
		out[key] = append(json.RawMessage(nil), value...)
	}
	return out
}
