package plan

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Reflection is test-only: new field shapes must be admitted explicitly rather
// than quietly falling outside the snapshot contract.
func cloneFixture(t *testing.T, typ reflect.Type, path, mode string) reflect.Value {
	t.Helper()
	v := reflect.New(typ).Elem()
	if typ == reflect.TypeFor[time.Time]() {
		v.Set(reflect.ValueOf(time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)))
		return v
	}
	switch typ.Kind() {
	case reflect.Struct:
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if f.PkgPath != "" {
				if typ == reflect.TypeFor[PlanDetail]() && (f.Name == "loadedStateBaseline" || f.Name == "loadedSlicesBaseline") {
					continue
				}
				t.Fatalf("%s.%s: unsupported private field", path, f.Name)
			}
			v.Field(i).Set(cloneFixture(t, f.Type, path+"."+f.Name, mode))
		}
	case reflect.Pointer:
		if mode != "nil" {
			v.Set(reflect.New(typ.Elem()))
			v.Elem().Set(cloneFixture(t, typ.Elem(), path+"*", mode))
		}
	case reflect.Slice:
		if mode != "nil" {
			n := 1
			if mode == "empty" {
				n = 0
			}
			v.Set(reflect.MakeSlice(typ, n, n))
			for i := 0; i < n; i++ {
				v.Index(i).Set(cloneFixture(t, typ.Elem(), path+"[]", mode))
			}
		}
	case reflect.Map:
		if typ.Key().Kind() != reflect.String {
			t.Fatalf("%s: unsupported map key %v", path, typ.Key())
		}
		if mode != "nil" {
			v.Set(reflect.MakeMap(typ))
			if mode != "empty" {
				key := reflect.New(typ.Key()).Elem()
				key.SetString("key")
				v.SetMapIndex(key, cloneFixture(t, typ.Elem(), path+"[key]", mode))
			}
		}
	case reflect.Interface:
		if typ != reflect.TypeFor[any]() {
			t.Fatalf("%s: unsupported interface %v", path, typ)
		}
		if mode != "nil" {
			v.Set(reflect.ValueOf([]any{map[string]any{"nested": []any{"value", nil, true, 2.5}}, map[string]any{}, []any{}, map[string]any(nil), []any(nil)}))
		}
	case reflect.String:
		v.SetString(path + " value")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int64:
		v.SetInt(7)
	case reflect.Float64:
		v.SetFloat(3.5)
	default:
		t.Fatalf("%s: unsupported shape %v", path, typ)
	}
	return v
}

func checkCloneStructure(t *testing.T, a, b reflect.Value, path string) {
	t.Helper()
	if a.Type() != b.Type() {
		t.Fatalf("%s: type changed", path)
	}
	if a.Type() == reflect.TypeFor[time.Time]() {
		if !reflect.DeepEqual(a.Interface(), b.Interface()) {
			t.Errorf("%s: time changed", path)
		}
		return
	}
	switch a.Kind() {
	case reflect.Struct:
		for i := 0; i < a.NumField(); i++ {
			f := a.Type().Field(i)
			if f.PkgPath != "" {
				if a.Type() == reflect.TypeFor[PlanDetail]() && (f.Name == "loadedStateBaseline" || f.Name == "loadedSlicesBaseline") {
					continue
				}
				t.Fatalf("%s.%s: unsupported private field", path, f.Name)
			}
			checkCloneStructure(t, a.Field(i), b.Field(i), path+"."+f.Name)
		}
	case reflect.Pointer, reflect.Interface:
		if a.IsNil() != b.IsNil() {
			t.Errorf("%s: nil changed", path)
			return
		}
		if a.IsNil() {
			return
		}
		if a.Kind() == reflect.Pointer && a.Pointer() == b.Pointer() {
			t.Errorf("%s: pointer aliases source", path)
		}
		checkCloneStructure(t, a.Elem(), b.Elem(), path+"*")
	case reflect.Slice, reflect.Map:
		if a.IsNil() != b.IsNil() && !cloneLegacyContainerRepresentation(path, a, b) {
			t.Errorf("%s: nil/empty changed", path)
		}
		if a.Len() != b.Len() {
			t.Errorf("%s: length changed", path)
			return
		}
		if a.Kind() == reflect.Map {
			if !a.IsNil() && a.Pointer() == b.Pointer() {
				t.Errorf("%s: map aliases source", path)
			}
			for _, key := range a.MapKeys() {
				value := b.MapIndex(key)
				if !value.IsValid() {
					t.Errorf("%s: missing key %v", path, key)
					continue
				}
				checkCloneStructure(t, a.MapIndex(key), value, fmt.Sprintf("%s[%s]", path, key.String()))
			}
		} else {
			if a.Len() > 0 && a.Pointer() == b.Pointer() {
				t.Errorf("%s: slice aliases source", path)
			}
			for i := 0; i < a.Len(); i++ {
				checkCloneStructure(t, a.Index(i), b.Index(i), fmt.Sprintf("%s[%d]", path, i))
			}
		}
	case reflect.String, reflect.Bool, reflect.Int, reflect.Int64, reflect.Float64:
		if !reflect.DeepEqual(a.Interface(), b.Interface()) {
			t.Errorf("%s: value changed", path)
		}
	default:
		t.Fatalf("%s: unsupported shape %v", path, a.Type())
	}
}

// These pre-existing helpers normalize empty containers; cloning is not a
// serialization migration. Keep the exceptions directional and field-specific.
func cloneLegacyContainerRepresentation(path string, a, b reflect.Value) bool {
	if a.Len() != 0 || b.Len() != 0 {
		return false
	}
	return (!a.IsNil() && b.IsNil() && (strings.HasSuffix(path, ".RuntimePrerequisites") || strings.HasSuffix(path, ".Sequence*.Relationships"))) ||
		(a.IsNil() && !b.IsNil() && strings.HasSuffix(path, ".Review*.Findings") && strings.Contains(path, "SingleMerge"))
}

func TestClonePlanDetailStructure(t *testing.T) {
	for _, mode := range []string{"populated", "nil", "empty"} {
		t.Run(mode, func(t *testing.T) {
			detail := cloneFixture(t, reflect.TypeFor[PlanDetail](), "PlanDetail", mode).Interface().(PlanDetail)
			// Private fields are accessed through typed package-local code, never unsafe.
			if mode != "nil" {
				state := cloneFixture(t, reflect.TypeFor[State](), "loadedStateBaseline", mode).Interface().(State)
				slices := cloneFixture(t, reflect.TypeFor[SlicesFile](), "loadedSlicesBaseline", mode).Interface().(SlicesFile)
				detail.loadedStateBaseline, detail.loadedSlicesBaseline = &state, &slices
			}
			clone := clonePlanDetail(&detail)
			checkCloneStructure(t, reflect.ValueOf(&detail), reflect.ValueOf(clone), "PlanDetail")
			checkCloneStructure(t, reflect.ValueOf(detail.loadedStateBaseline), reflect.ValueOf(clone.loadedStateBaseline), "PlanDetail.loadedStateBaseline")
			checkCloneStructure(t, reflect.ValueOf(detail.loadedSlicesBaseline), reflect.ValueOf(clone.loadedSlicesBaseline), "PlanDetail.loadedSlicesBaseline")
		})
	}
}
