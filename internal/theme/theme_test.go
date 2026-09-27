package theme

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestLookupAndNames(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{"tokyonight", "tokyonight"}, {" GruvBox ", "gruvbox"}, {"default", "tokyonight"},
	} {
		got, ok := Lookup(test.input)
		if !ok || got.Name() != test.want {
			t.Errorf("Lookup(%q) = %q, %t; want %q", test.input, got.Name(), ok, test.want)
		}
	}
	for _, name := range []string{"nope", "", "   "} {
		if _, ok := Lookup(name); ok {
			t.Errorf("Lookup(%q) succeeded", name)
		}
	}
	want := []string{"tokyonight", "gruvbox"}
	if got := Names(); !slices.Equal(got, want) || got[0] != Default().Name() {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	names := Names()
	names[0] = "mutated"
	if got := Names(); !slices.Equal(got, want) {
		t.Fatalf("Names() changed after caller mutation: %v", got)
	}
}

func TestShippedThemesAssignEveryRole(t *testing.T) {
	for _, name := range Names() {
		th, ok := Lookup(name)
		if !ok {
			t.Fatalf("theme %q is not available", name)
		}
		for profile := ProfileNone; profile <= ProfileTrueColor; profile++ {
			for role := RoleAccent; role < roleCount; role++ {
				color, assigned := th.Palette(profile).Color(role)
				if !assigned || color.Profile != profile {
					t.Fatalf("%s/%s role %d = %+v, assigned %t", name, profile, role, color, assigned)
				}
				if profile == ProfileANSI16 && (color.Index != th.specs[role].ansi16 || color.Index > 15) {
					t.Errorf("%s role %d has invalid authored ANSI16 index %d", name, role, color.Index)
				}
			}
		}
	}
}

func TestGruvboxDiffersInEveryRole(t *testing.T) {
	th, ok := Lookup("gruvbox")
	if !ok {
		t.Fatal("gruvbox is not available")
	}
	for role := RoleAccent; role < roleCount; role++ {
		if strings.EqualFold(th.specs[role].hex, Default().specs[role].hex) {
			t.Errorf("role %d has the same hex in both themes", role)
		}
	}
}

func TestSequenceParity(t *testing.T) {
	if len(sequenceParity) != int(roleCount)*4 {
		t.Fatal("incomplete parity capture")
	}
	for _, test := range sequenceParity {
		t.Run(fmt.Sprintf("%s/role%d", test.profile, test.role), func(t *testing.T) {
			for _, th := range []Theme{Default(), {}} {
				p := th.Palette(test.profile)
				const row = "x\x1b[0my"
				got := []string{p.Paint(test.role, "x"), p.FillRow(test.role, row), p.FillRowWithText(test.role, test.role, false, row), p.FillRowWithText(test.role, test.role, true, row), p.SelectRow(row)}
				want := []string{test.paint, test.fill, test.text, test.bold, test.selection}
				for i := range got {
					if got[i] != want[i] {
						t.Errorf("operation %d = %q, want %q", i, got[i], want[i])
					}
				}
			}
		})
	}
}

func TestDefaultAndZeroTheme(t *testing.T) {
	if Default().Name() != "tokyonight" || (Theme{}).Name() != Default().Name() {
		t.Fatal("default theme name mismatch")
	}
	for profile := ProfileNone; profile <= ProfileTrueColor; profile++ {
		for role := RoleAccent; role < roleCount; role++ {
			want, ok := Default().Palette(profile).Color(role)
			if !ok {
				t.Fatalf("unassigned role %d", role)
			}
			for _, p := range []Palette{(Theme{}).Palette(profile), {Profile: profile}} {
				if p.Enabled() != profile.Enabled() {
					t.Fatal("enabled mismatch")
				}
				if got, ok := p.Color(role); !ok || got != want {
					t.Fatalf("zero theme Color(%d) = %#v, %t, want %#v", role, got, ok, want)
				}
				if got := p.MustColor(role); got != want {
					t.Fatalf("MustColor(%d) = %#v, want %#v", role, got, want)
				}
				if p.Paint(role, "") != "" || p.FillRow(role, "") != "" || p.FillRowWithText(role, role, true, "") != "" || p.SelectRow("") != "" {
					t.Fatal("empty text changed")
				}
			}
		}
	}
}

func TestANSI16UsesAuthoredIndex(t *testing.T) {
	p := Default().Palette(ProfileANSI16)
	differs := false
	for role := RoleAccent; role < roleCount; role++ {
		spec := Default().specs[role]
		got, ok := p.Color(role)
		if !ok || got.Index != spec.ansi16 {
			t.Fatalf("role %d = %#v, want index %d", role, got, spec.ansi16)
		}
		converted, err := ProfileANSI16.Convert(spec.hex)
		if err != nil {
			t.Fatal(err)
		}
		differs = differs || converted.Index != got.Index
	}
	if !differs {
		t.Fatal("test must distinguish authored and converted indexes")
	}
}

func TestUnknownRole(t *testing.T) {
	p := Default().Palette(ProfileTrueColor)
	if _, ok := p.Color(roleCount); ok {
		t.Fatal("unknown role assigned")
	}
	if got := p.Paint(roleCount, "plain"); got != "plain" {
		t.Fatalf("unknown role paint = %q", got)
	}
	defer func() {
		if recover() == nil {
			t.Error("MustColor should panic for unknown role")
		}
	}()
	p.MustColor(roleCount)
}

// Captured from internal/tui before extraction; do not regenerate from the new implementation.
var sequenceParity = []struct {
	profile                            Profile
	role                               Role
	paint, fill, text, bold, selection string
}{
	{0, 0, "x", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my"},
	{0, 1, "x", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my"},
	{0, 2, "x", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my"},
	{0, 3, "x", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my"},
	{0, 4, "x", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my"},
	{0, 5, "x", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my"},
	{0, 6, "x", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my"},
	{0, 7, "x", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my"},
	{0, 8, "x", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my"},
	{0, 9, "x", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my"},
	{0, 10, "x", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my"},
	{0, 11, "x", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my"},
	{0, 12, "x", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my"},
	{0, 13, "x", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my"},
	{0, 14, "x", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my"},
	{0, 15, "x", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my"},
	{0, 16, "x", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my"},
	{0, 17, "x", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my"},
	{0, 18, "x", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my"},
	{0, 19, "x", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my"},
	{0, 20, "x", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my"},
	{0, 21, "x", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my"},
	{0, 22, "x", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my"},
	{0, 23, "x", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my"},
	{0, 24, "x", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my"},
	{0, 25, "x", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my"},
	{0, 26, "x", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my"},
	{0, 27, "x", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my"},
	{0, 28, "x", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my"},
	{0, 29, "x", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my"},
	{0, 30, "x", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my"},
	{0, 31, "x", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my"},
	{0, 32, "x", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my"},
	{0, 33, "x", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my", "x\x1b[0my"},
	{1, 0, "\x1b[96mx\x1b[0m", "\x1b[106mx\x1b[0m\x1b[106my\x1b[0m", "\x1b[106m\x1b[96mx\x1b[0m\x1b[106m\x1b[96my\x1b[0m", "\x1b[1m\x1b[106m\x1b[96mx\x1b[0m\x1b[1m\x1b[106m\x1b[96my\x1b[0m", "\x1b[1m\x1b[44mx\x1b[0m\x1b[1m\x1b[44my\x1b[0m"},
	{1, 1, "\x1b[93mx\x1b[0m", "\x1b[103mx\x1b[0m\x1b[103my\x1b[0m", "\x1b[103m\x1b[93mx\x1b[0m\x1b[103m\x1b[93my\x1b[0m", "\x1b[1m\x1b[103m\x1b[93mx\x1b[0m\x1b[1m\x1b[103m\x1b[93my\x1b[0m", "\x1b[1m\x1b[44mx\x1b[0m\x1b[1m\x1b[44my\x1b[0m"},
	{1, 2, "\x1b[92mx\x1b[0m", "\x1b[102mx\x1b[0m\x1b[102my\x1b[0m", "\x1b[102m\x1b[92mx\x1b[0m\x1b[102m\x1b[92my\x1b[0m", "\x1b[1m\x1b[102m\x1b[92mx\x1b[0m\x1b[1m\x1b[102m\x1b[92my\x1b[0m", "\x1b[1m\x1b[44mx\x1b[0m\x1b[1m\x1b[44my\x1b[0m"},
	{1, 3, "\x1b[95mx\x1b[0m", "\x1b[105mx\x1b[0m\x1b[105my\x1b[0m", "\x1b[105m\x1b[95mx\x1b[0m\x1b[105m\x1b[95my\x1b[0m", "\x1b[1m\x1b[105m\x1b[95mx\x1b[0m\x1b[1m\x1b[105m\x1b[95my\x1b[0m", "\x1b[1m\x1b[44mx\x1b[0m\x1b[1m\x1b[44my\x1b[0m"},
	{1, 4, "\x1b[97mx\x1b[0m", "\x1b[107mx\x1b[0m\x1b[107my\x1b[0m", "\x1b[107m\x1b[97mx\x1b[0m\x1b[107m\x1b[97my\x1b[0m", "\x1b[1m\x1b[107m\x1b[97mx\x1b[0m\x1b[1m\x1b[107m\x1b[97my\x1b[0m", "\x1b[1m\x1b[44mx\x1b[0m\x1b[1m\x1b[44my\x1b[0m"},
	{1, 5, "\x1b[96mx\x1b[0m", "\x1b[106mx\x1b[0m\x1b[106my\x1b[0m", "\x1b[106m\x1b[96mx\x1b[0m\x1b[106m\x1b[96my\x1b[0m", "\x1b[1m\x1b[106m\x1b[96mx\x1b[0m\x1b[1m\x1b[106m\x1b[96my\x1b[0m", "\x1b[1m\x1b[44mx\x1b[0m\x1b[1m\x1b[44my\x1b[0m"},
	{1, 6, "\x1b[92mx\x1b[0m", "\x1b[102mx\x1b[0m\x1b[102my\x1b[0m", "\x1b[102m\x1b[92mx\x1b[0m\x1b[102m\x1b[92my\x1b[0m", "\x1b[1m\x1b[102m\x1b[92mx\x1b[0m\x1b[1m\x1b[102m\x1b[92my\x1b[0m", "\x1b[1m\x1b[44mx\x1b[0m\x1b[1m\x1b[44my\x1b[0m"},
	{1, 7, "\x1b[94mx\x1b[0m", "\x1b[104mx\x1b[0m\x1b[104my\x1b[0m", "\x1b[104m\x1b[94mx\x1b[0m\x1b[104m\x1b[94my\x1b[0m", "\x1b[1m\x1b[104m\x1b[94mx\x1b[0m\x1b[1m\x1b[104m\x1b[94my\x1b[0m", "\x1b[1m\x1b[44mx\x1b[0m\x1b[1m\x1b[44my\x1b[0m"},
	{1, 8, "\x1b[90mx\x1b[0m", "\x1b[100mx\x1b[0m\x1b[100my\x1b[0m", "\x1b[100m\x1b[90mx\x1b[0m\x1b[100m\x1b[90my\x1b[0m", "\x1b[1m\x1b[100m\x1b[90mx\x1b[0m\x1b[1m\x1b[100m\x1b[90my\x1b[0m", "\x1b[1m\x1b[44mx\x1b[0m\x1b[1m\x1b[44my\x1b[0m"},
	{1, 9, "\x1b[30mx\x1b[0m", "\x1b[40mx\x1b[0m\x1b[40my\x1b[0m", "\x1b[40m\x1b[30mx\x1b[0m\x1b[40m\x1b[30my\x1b[0m", "\x1b[1m\x1b[40m\x1b[30mx\x1b[0m\x1b[1m\x1b[40m\x1b[30my\x1b[0m", "\x1b[1m\x1b[44mx\x1b[0m\x1b[1m\x1b[44my\x1b[0m"},
	{1, 10, "\x1b[30mx\x1b[0m", "\x1b[40mx\x1b[0m\x1b[40my\x1b[0m", "\x1b[40m\x1b[30mx\x1b[0m\x1b[40m\x1b[30my\x1b[0m", "\x1b[1m\x1b[40m\x1b[30mx\x1b[0m\x1b[1m\x1b[40m\x1b[30my\x1b[0m", "\x1b[1m\x1b[44mx\x1b[0m\x1b[1m\x1b[44my\x1b[0m"},
	{1, 11, "\x1b[30mx\x1b[0m", "\x1b[40mx\x1b[0m\x1b[40my\x1b[0m", "\x1b[40m\x1b[30mx\x1b[0m\x1b[40m\x1b[30my\x1b[0m", "\x1b[1m\x1b[40m\x1b[30mx\x1b[0m\x1b[1m\x1b[40m\x1b[30my\x1b[0m", "\x1b[1m\x1b[44mx\x1b[0m\x1b[1m\x1b[44my\x1b[0m"},
	{1, 12, "\x1b[90mx\x1b[0m", "\x1b[100mx\x1b[0m\x1b[100my\x1b[0m", "\x1b[100m\x1b[90mx\x1b[0m\x1b[100m\x1b[90my\x1b[0m", "\x1b[1m\x1b[100m\x1b[90mx\x1b[0m\x1b[1m\x1b[100m\x1b[90my\x1b[0m", "\x1b[1m\x1b[44mx\x1b[0m\x1b[1m\x1b[44my\x1b[0m"},
	{1, 13, "\x1b[34mx\x1b[0m", "\x1b[44mx\x1b[0m\x1b[44my\x1b[0m", "\x1b[44m\x1b[34mx\x1b[0m\x1b[44m\x1b[34my\x1b[0m", "\x1b[1m\x1b[44m\x1b[34mx\x1b[0m\x1b[1m\x1b[44m\x1b[34my\x1b[0m", "\x1b[1m\x1b[44mx\x1b[0m\x1b[1m\x1b[44my\x1b[0m"},
	{1, 14, "\x1b[97mx\x1b[0m", "\x1b[107mx\x1b[0m\x1b[107my\x1b[0m", "\x1b[107m\x1b[97mx\x1b[0m\x1b[107m\x1b[97my\x1b[0m", "\x1b[1m\x1b[107m\x1b[97mx\x1b[0m\x1b[1m\x1b[107m\x1b[97my\x1b[0m", "\x1b[1m\x1b[44mx\x1b[0m\x1b[1m\x1b[44my\x1b[0m"},
	{1, 15, "\x1b[90mx\x1b[0m", "\x1b[100mx\x1b[0m\x1b[100my\x1b[0m", "\x1b[100m\x1b[90mx\x1b[0m\x1b[100m\x1b[90my\x1b[0m", "\x1b[1m\x1b[100m\x1b[90mx\x1b[0m\x1b[1m\x1b[100m\x1b[90my\x1b[0m", "\x1b[1m\x1b[44mx\x1b[0m\x1b[1m\x1b[44my\x1b[0m"},
	{1, 16, "\x1b[90mx\x1b[0m", "\x1b[100mx\x1b[0m\x1b[100my\x1b[0m", "\x1b[100m\x1b[90mx\x1b[0m\x1b[100m\x1b[90my\x1b[0m", "\x1b[1m\x1b[100m\x1b[90mx\x1b[0m\x1b[1m\x1b[100m\x1b[90my\x1b[0m", "\x1b[1m\x1b[44mx\x1b[0m\x1b[1m\x1b[44my\x1b[0m"},
	{1, 17, "\x1b[30mx\x1b[0m", "\x1b[40mx\x1b[0m\x1b[40my\x1b[0m", "\x1b[40m\x1b[30mx\x1b[0m\x1b[40m\x1b[30my\x1b[0m", "\x1b[1m\x1b[40m\x1b[30mx\x1b[0m\x1b[1m\x1b[40m\x1b[30my\x1b[0m", "\x1b[1m\x1b[44mx\x1b[0m\x1b[1m\x1b[44my\x1b[0m"},
	{1, 18, "\x1b[97mx\x1b[0m", "\x1b[107mx\x1b[0m\x1b[107my\x1b[0m", "\x1b[107m\x1b[97mx\x1b[0m\x1b[107m\x1b[97my\x1b[0m", "\x1b[1m\x1b[107m\x1b[97mx\x1b[0m\x1b[1m\x1b[107m\x1b[97my\x1b[0m", "\x1b[1m\x1b[44mx\x1b[0m\x1b[1m\x1b[44my\x1b[0m"},
	{1, 19, "\x1b[37mx\x1b[0m", "\x1b[47mx\x1b[0m\x1b[47my\x1b[0m", "\x1b[47m\x1b[37mx\x1b[0m\x1b[47m\x1b[37my\x1b[0m", "\x1b[1m\x1b[47m\x1b[37mx\x1b[0m\x1b[1m\x1b[47m\x1b[37my\x1b[0m", "\x1b[1m\x1b[44mx\x1b[0m\x1b[1m\x1b[44my\x1b[0m"},
	{1, 20, "\x1b[90mx\x1b[0m", "\x1b[100mx\x1b[0m\x1b[100my\x1b[0m", "\x1b[100m\x1b[90mx\x1b[0m\x1b[100m\x1b[90my\x1b[0m", "\x1b[1m\x1b[100m\x1b[90mx\x1b[0m\x1b[1m\x1b[100m\x1b[90my\x1b[0m", "\x1b[1m\x1b[44mx\x1b[0m\x1b[1m\x1b[44my\x1b[0m"},
	{1, 21, "\x1b[90mx\x1b[0m", "\x1b[100mx\x1b[0m\x1b[100my\x1b[0m", "\x1b[100m\x1b[90mx\x1b[0m\x1b[100m\x1b[90my\x1b[0m", "\x1b[1m\x1b[100m\x1b[90mx\x1b[0m\x1b[1m\x1b[100m\x1b[90my\x1b[0m", "\x1b[1m\x1b[44mx\x1b[0m\x1b[1m\x1b[44my\x1b[0m"},
	{1, 22, "\x1b[92mx\x1b[0m", "\x1b[102mx\x1b[0m\x1b[102my\x1b[0m", "\x1b[102m\x1b[92mx\x1b[0m\x1b[102m\x1b[92my\x1b[0m", "\x1b[1m\x1b[102m\x1b[92mx\x1b[0m\x1b[1m\x1b[102m\x1b[92my\x1b[0m", "\x1b[1m\x1b[44mx\x1b[0m\x1b[1m\x1b[44my\x1b[0m"},
	{1, 23, "\x1b[94mx\x1b[0m", "\x1b[104mx\x1b[0m\x1b[104my\x1b[0m", "\x1b[104m\x1b[94mx\x1b[0m\x1b[104m\x1b[94my\x1b[0m", "\x1b[1m\x1b[104m\x1b[94mx\x1b[0m\x1b[1m\x1b[104m\x1b[94my\x1b[0m", "\x1b[1m\x1b[44mx\x1b[0m\x1b[1m\x1b[44my\x1b[0m"},
	{1, 24, "\x1b[93mx\x1b[0m", "\x1b[103mx\x1b[0m\x1b[103my\x1b[0m", "\x1b[103m\x1b[93mx\x1b[0m\x1b[103m\x1b[93my\x1b[0m", "\x1b[1m\x1b[103m\x1b[93mx\x1b[0m\x1b[1m\x1b[103m\x1b[93my\x1b[0m", "\x1b[1m\x1b[44mx\x1b[0m\x1b[1m\x1b[44my\x1b[0m"},
	{1, 25, "\x1b[91mx\x1b[0m", "\x1b[101mx\x1b[0m\x1b[101my\x1b[0m", "\x1b[101m\x1b[91mx\x1b[0m\x1b[101m\x1b[91my\x1b[0m", "\x1b[1m\x1b[101m\x1b[91mx\x1b[0m\x1b[1m\x1b[101m\x1b[91my\x1b[0m", "\x1b[1m\x1b[44mx\x1b[0m\x1b[1m\x1b[44my\x1b[0m"},
	{1, 26, "\x1b[90mx\x1b[0m", "\x1b[100mx\x1b[0m\x1b[100my\x1b[0m", "\x1b[100m\x1b[90mx\x1b[0m\x1b[100m\x1b[90my\x1b[0m", "\x1b[1m\x1b[100m\x1b[90mx\x1b[0m\x1b[1m\x1b[100m\x1b[90my\x1b[0m", "\x1b[1m\x1b[44mx\x1b[0m\x1b[1m\x1b[44my\x1b[0m"},
	{1, 27, "\x1b[34mx\x1b[0m", "\x1b[44mx\x1b[0m\x1b[44my\x1b[0m", "\x1b[44m\x1b[34mx\x1b[0m\x1b[44m\x1b[34my\x1b[0m", "\x1b[1m\x1b[44m\x1b[34mx\x1b[0m\x1b[1m\x1b[44m\x1b[34my\x1b[0m", "\x1b[1m\x1b[44mx\x1b[0m\x1b[1m\x1b[44my\x1b[0m"},
	{1, 28, "\x1b[36mx\x1b[0m", "\x1b[46mx\x1b[0m\x1b[46my\x1b[0m", "\x1b[46m\x1b[36mx\x1b[0m\x1b[46m\x1b[36my\x1b[0m", "\x1b[1m\x1b[46m\x1b[36mx\x1b[0m\x1b[1m\x1b[46m\x1b[36my\x1b[0m", "\x1b[1m\x1b[44mx\x1b[0m\x1b[1m\x1b[44my\x1b[0m"},
	{1, 29, "\x1b[90mx\x1b[0m", "\x1b[100mx\x1b[0m\x1b[100my\x1b[0m", "\x1b[100m\x1b[90mx\x1b[0m\x1b[100m\x1b[90my\x1b[0m", "\x1b[1m\x1b[100m\x1b[90mx\x1b[0m\x1b[1m\x1b[100m\x1b[90my\x1b[0m", "\x1b[1m\x1b[44mx\x1b[0m\x1b[1m\x1b[44my\x1b[0m"},
	{1, 30, "\x1b[90mx\x1b[0m", "\x1b[100mx\x1b[0m\x1b[100my\x1b[0m", "\x1b[100m\x1b[90mx\x1b[0m\x1b[100m\x1b[90my\x1b[0m", "\x1b[1m\x1b[100m\x1b[90mx\x1b[0m\x1b[1m\x1b[100m\x1b[90my\x1b[0m", "\x1b[1m\x1b[44mx\x1b[0m\x1b[1m\x1b[44my\x1b[0m"},
	{1, 31, "\x1b[37mx\x1b[0m", "\x1b[47mx\x1b[0m\x1b[47my\x1b[0m", "\x1b[47m\x1b[37mx\x1b[0m\x1b[47m\x1b[37my\x1b[0m", "\x1b[1m\x1b[47m\x1b[37mx\x1b[0m\x1b[1m\x1b[47m\x1b[37my\x1b[0m", "\x1b[1m\x1b[44mx\x1b[0m\x1b[1m\x1b[44my\x1b[0m"},
	{1, 32, "\x1b[97mx\x1b[0m", "\x1b[107mx\x1b[0m\x1b[107my\x1b[0m", "\x1b[107m\x1b[97mx\x1b[0m\x1b[107m\x1b[97my\x1b[0m", "\x1b[1m\x1b[107m\x1b[97mx\x1b[0m\x1b[1m\x1b[107m\x1b[97my\x1b[0m", "\x1b[1m\x1b[44mx\x1b[0m\x1b[1m\x1b[44my\x1b[0m"},
	{1, 33, "\x1b[34mx\x1b[0m", "\x1b[44mx\x1b[0m\x1b[44my\x1b[0m", "\x1b[44m\x1b[34mx\x1b[0m\x1b[44m\x1b[34my\x1b[0m", "\x1b[1m\x1b[44m\x1b[34mx\x1b[0m\x1b[1m\x1b[44m\x1b[34my\x1b[0m", "\x1b[1m\x1b[44mx\x1b[0m\x1b[1m\x1b[44my\x1b[0m"},
	{2, 0, "\x1b[38;5;79mx\x1b[0m", "\x1b[48;5;79mx\x1b[0m\x1b[48;5;79my\x1b[0m", "\x1b[48;5;79m\x1b[38;5;79mx\x1b[0m\x1b[48;5;79m\x1b[38;5;79my\x1b[0m", "\x1b[1m\x1b[48;5;79m\x1b[38;5;79mx\x1b[0m\x1b[1m\x1b[48;5;79m\x1b[38;5;79my\x1b[0m", "\x1b[1m\x1b[48;5;236mx\x1b[0m\x1b[1m\x1b[48;5;236my\x1b[0m"},
	{2, 1, "\x1b[38;5;214mx\x1b[0m", "\x1b[48;5;214mx\x1b[0m\x1b[48;5;214my\x1b[0m", "\x1b[48;5;214m\x1b[38;5;214mx\x1b[0m\x1b[48;5;214m\x1b[38;5;214my\x1b[0m", "\x1b[1m\x1b[48;5;214m\x1b[38;5;214mx\x1b[0m\x1b[1m\x1b[48;5;214m\x1b[38;5;214my\x1b[0m", "\x1b[1m\x1b[48;5;236mx\x1b[0m\x1b[1m\x1b[48;5;236my\x1b[0m"},
	{2, 2, "\x1b[38;5;113mx\x1b[0m", "\x1b[48;5;113mx\x1b[0m\x1b[48;5;113my\x1b[0m", "\x1b[48;5;113m\x1b[38;5;113mx\x1b[0m\x1b[48;5;113m\x1b[38;5;113my\x1b[0m", "\x1b[1m\x1b[48;5;113m\x1b[38;5;113mx\x1b[0m\x1b[1m\x1b[48;5;113m\x1b[38;5;113my\x1b[0m", "\x1b[1m\x1b[48;5;236mx\x1b[0m\x1b[1m\x1b[48;5;236my\x1b[0m"},
	{2, 3, "\x1b[38;5;147mx\x1b[0m", "\x1b[48;5;147mx\x1b[0m\x1b[48;5;147my\x1b[0m", "\x1b[48;5;147m\x1b[38;5;147mx\x1b[0m\x1b[48;5;147m\x1b[38;5;147my\x1b[0m", "\x1b[1m\x1b[48;5;147m\x1b[38;5;147mx\x1b[0m\x1b[1m\x1b[48;5;147m\x1b[38;5;147my\x1b[0m", "\x1b[1m\x1b[48;5;236mx\x1b[0m\x1b[1m\x1b[48;5;236my\x1b[0m"},
	{2, 4, "\x1b[38;5;183mx\x1b[0m", "\x1b[48;5;183mx\x1b[0m\x1b[48;5;183my\x1b[0m", "\x1b[48;5;183m\x1b[38;5;183mx\x1b[0m\x1b[48;5;183m\x1b[38;5;183my\x1b[0m", "\x1b[1m\x1b[48;5;183m\x1b[38;5;183mx\x1b[0m\x1b[1m\x1b[48;5;183m\x1b[38;5;183my\x1b[0m", "\x1b[1m\x1b[48;5;236mx\x1b[0m\x1b[1m\x1b[48;5;236my\x1b[0m"},
	{2, 5, "\x1b[38;5;79mx\x1b[0m", "\x1b[48;5;79mx\x1b[0m\x1b[48;5;79my\x1b[0m", "\x1b[48;5;79m\x1b[38;5;79mx\x1b[0m\x1b[48;5;79m\x1b[38;5;79my\x1b[0m", "\x1b[1m\x1b[48;5;79m\x1b[38;5;79mx\x1b[0m\x1b[1m\x1b[48;5;79m\x1b[38;5;79my\x1b[0m", "\x1b[1m\x1b[48;5;236mx\x1b[0m\x1b[1m\x1b[48;5;236my\x1b[0m"},
	{2, 6, "\x1b[38;5;149mx\x1b[0m", "\x1b[48;5;149mx\x1b[0m\x1b[48;5;149my\x1b[0m", "\x1b[48;5;149m\x1b[38;5;149mx\x1b[0m\x1b[48;5;149m\x1b[38;5;149my\x1b[0m", "\x1b[1m\x1b[48;5;149m\x1b[38;5;149mx\x1b[0m\x1b[1m\x1b[48;5;149m\x1b[38;5;149my\x1b[0m", "\x1b[1m\x1b[48;5;236mx\x1b[0m\x1b[1m\x1b[48;5;236my\x1b[0m"},
	{2, 7, "\x1b[38;5;111mx\x1b[0m", "\x1b[48;5;111mx\x1b[0m\x1b[48;5;111my\x1b[0m", "\x1b[48;5;111m\x1b[38;5;111mx\x1b[0m\x1b[48;5;111m\x1b[38;5;111my\x1b[0m", "\x1b[1m\x1b[48;5;111m\x1b[38;5;111mx\x1b[0m\x1b[1m\x1b[48;5;111m\x1b[38;5;111my\x1b[0m", "\x1b[1m\x1b[48;5;236mx\x1b[0m\x1b[1m\x1b[48;5;236my\x1b[0m"},
	{2, 8, "\x1b[38;5;60mx\x1b[0m", "\x1b[48;5;60mx\x1b[0m\x1b[48;5;60my\x1b[0m", "\x1b[48;5;60m\x1b[38;5;60mx\x1b[0m\x1b[48;5;60m\x1b[38;5;60my\x1b[0m", "\x1b[1m\x1b[48;5;60m\x1b[38;5;60mx\x1b[0m\x1b[1m\x1b[48;5;60m\x1b[38;5;60my\x1b[0m", "\x1b[1m\x1b[48;5;236mx\x1b[0m\x1b[1m\x1b[48;5;236my\x1b[0m"},
	{2, 9, "\x1b[38;5;234mx\x1b[0m", "\x1b[48;5;234mx\x1b[0m\x1b[48;5;234my\x1b[0m", "\x1b[48;5;234m\x1b[38;5;234mx\x1b[0m\x1b[48;5;234m\x1b[38;5;234my\x1b[0m", "\x1b[1m\x1b[48;5;234m\x1b[38;5;234mx\x1b[0m\x1b[1m\x1b[48;5;234m\x1b[38;5;234my\x1b[0m", "\x1b[1m\x1b[48;5;236mx\x1b[0m\x1b[1m\x1b[48;5;236my\x1b[0m"},
	{2, 10, "\x1b[38;5;234mx\x1b[0m", "\x1b[48;5;234mx\x1b[0m\x1b[48;5;234my\x1b[0m", "\x1b[48;5;234m\x1b[38;5;234mx\x1b[0m\x1b[48;5;234m\x1b[38;5;234my\x1b[0m", "\x1b[1m\x1b[48;5;234m\x1b[38;5;234mx\x1b[0m\x1b[1m\x1b[48;5;234m\x1b[38;5;234my\x1b[0m", "\x1b[1m\x1b[48;5;236mx\x1b[0m\x1b[1m\x1b[48;5;236my\x1b[0m"},
	{2, 11, "\x1b[38;5;234mx\x1b[0m", "\x1b[48;5;234mx\x1b[0m\x1b[48;5;234my\x1b[0m", "\x1b[48;5;234m\x1b[38;5;234mx\x1b[0m\x1b[48;5;234m\x1b[38;5;234my\x1b[0m", "\x1b[1m\x1b[48;5;234m\x1b[38;5;234mx\x1b[0m\x1b[1m\x1b[48;5;234m\x1b[38;5;234my\x1b[0m", "\x1b[1m\x1b[48;5;236mx\x1b[0m\x1b[1m\x1b[48;5;236my\x1b[0m"},
	{2, 12, "\x1b[38;5;67mx\x1b[0m", "\x1b[48;5;67mx\x1b[0m\x1b[48;5;67my\x1b[0m", "\x1b[48;5;67m\x1b[38;5;67mx\x1b[0m\x1b[48;5;67m\x1b[38;5;67my\x1b[0m", "\x1b[1m\x1b[48;5;67m\x1b[38;5;67mx\x1b[0m\x1b[1m\x1b[48;5;67m\x1b[38;5;67my\x1b[0m", "\x1b[1m\x1b[48;5;236mx\x1b[0m\x1b[1m\x1b[48;5;236my\x1b[0m"},
	{2, 13, "\x1b[38;5;237mx\x1b[0m", "\x1b[48;5;237mx\x1b[0m\x1b[48;5;237my\x1b[0m", "\x1b[48;5;237m\x1b[38;5;237mx\x1b[0m\x1b[48;5;237m\x1b[38;5;237my\x1b[0m", "\x1b[1m\x1b[48;5;237m\x1b[38;5;237mx\x1b[0m\x1b[1m\x1b[48;5;237m\x1b[38;5;237my\x1b[0m", "\x1b[1m\x1b[48;5;236mx\x1b[0m\x1b[1m\x1b[48;5;236my\x1b[0m"},
	{2, 14, "\x1b[38;5;153mx\x1b[0m", "\x1b[48;5;153mx\x1b[0m\x1b[48;5;153my\x1b[0m", "\x1b[48;5;153m\x1b[38;5;153mx\x1b[0m\x1b[48;5;153m\x1b[38;5;153my\x1b[0m", "\x1b[1m\x1b[48;5;153m\x1b[38;5;153mx\x1b[0m\x1b[1m\x1b[48;5;153m\x1b[38;5;153my\x1b[0m", "\x1b[1m\x1b[48;5;236mx\x1b[0m\x1b[1m\x1b[48;5;236my\x1b[0m"},
	{2, 15, "\x1b[38;5;60mx\x1b[0m", "\x1b[48;5;60mx\x1b[0m\x1b[48;5;60my\x1b[0m", "\x1b[48;5;60m\x1b[38;5;60mx\x1b[0m\x1b[48;5;60m\x1b[38;5;60my\x1b[0m", "\x1b[1m\x1b[48;5;60m\x1b[38;5;60mx\x1b[0m\x1b[1m\x1b[48;5;60m\x1b[38;5;60my\x1b[0m", "\x1b[1m\x1b[48;5;236mx\x1b[0m\x1b[1m\x1b[48;5;236my\x1b[0m"},
	{2, 16, "\x1b[38;5;60mx\x1b[0m", "\x1b[48;5;60mx\x1b[0m\x1b[48;5;60my\x1b[0m", "\x1b[48;5;60m\x1b[38;5;60mx\x1b[0m\x1b[48;5;60m\x1b[38;5;60my\x1b[0m", "\x1b[1m\x1b[48;5;60m\x1b[38;5;60mx\x1b[0m\x1b[1m\x1b[48;5;60m\x1b[38;5;60my\x1b[0m", "\x1b[1m\x1b[48;5;236mx\x1b[0m\x1b[1m\x1b[48;5;236my\x1b[0m"},
	{2, 17, "\x1b[38;5;234mx\x1b[0m", "\x1b[48;5;234mx\x1b[0m\x1b[48;5;234my\x1b[0m", "\x1b[48;5;234m\x1b[38;5;234mx\x1b[0m\x1b[48;5;234m\x1b[38;5;234my\x1b[0m", "\x1b[1m\x1b[48;5;234m\x1b[38;5;234mx\x1b[0m\x1b[1m\x1b[48;5;234m\x1b[38;5;234my\x1b[0m", "\x1b[1m\x1b[48;5;236mx\x1b[0m\x1b[1m\x1b[48;5;236my\x1b[0m"},
	{2, 18, "\x1b[38;5;153mx\x1b[0m", "\x1b[48;5;153mx\x1b[0m\x1b[48;5;153my\x1b[0m", "\x1b[48;5;153m\x1b[38;5;153mx\x1b[0m\x1b[48;5;153m\x1b[38;5;153my\x1b[0m", "\x1b[1m\x1b[48;5;153m\x1b[38;5;153mx\x1b[0m\x1b[1m\x1b[48;5;153m\x1b[38;5;153my\x1b[0m", "\x1b[1m\x1b[48;5;236mx\x1b[0m\x1b[1m\x1b[48;5;236my\x1b[0m"},
	{2, 19, "\x1b[38;5;146mx\x1b[0m", "\x1b[48;5;146mx\x1b[0m\x1b[48;5;146my\x1b[0m", "\x1b[48;5;146m\x1b[38;5;146mx\x1b[0m\x1b[48;5;146m\x1b[38;5;146my\x1b[0m", "\x1b[1m\x1b[48;5;146m\x1b[38;5;146mx\x1b[0m\x1b[1m\x1b[48;5;146m\x1b[38;5;146my\x1b[0m", "\x1b[1m\x1b[48;5;236mx\x1b[0m\x1b[1m\x1b[48;5;236my\x1b[0m"},
	{2, 20, "\x1b[38;5;60mx\x1b[0m", "\x1b[48;5;60mx\x1b[0m\x1b[48;5;60my\x1b[0m", "\x1b[48;5;60m\x1b[38;5;60mx\x1b[0m\x1b[48;5;60m\x1b[38;5;60my\x1b[0m", "\x1b[1m\x1b[48;5;60m\x1b[38;5;60mx\x1b[0m\x1b[1m\x1b[48;5;60m\x1b[38;5;60my\x1b[0m", "\x1b[1m\x1b[48;5;236mx\x1b[0m\x1b[1m\x1b[48;5;236my\x1b[0m"},
	{2, 21, "\x1b[38;5;67mx\x1b[0m", "\x1b[48;5;67mx\x1b[0m\x1b[48;5;67my\x1b[0m", "\x1b[48;5;67m\x1b[38;5;67mx\x1b[0m\x1b[48;5;67m\x1b[38;5;67my\x1b[0m", "\x1b[1m\x1b[48;5;67m\x1b[38;5;67mx\x1b[0m\x1b[1m\x1b[48;5;67m\x1b[38;5;67my\x1b[0m", "\x1b[1m\x1b[48;5;236mx\x1b[0m\x1b[1m\x1b[48;5;236my\x1b[0m"},
	{2, 22, "\x1b[38;5;149mx\x1b[0m", "\x1b[48;5;149mx\x1b[0m\x1b[48;5;149my\x1b[0m", "\x1b[48;5;149m\x1b[38;5;149mx\x1b[0m\x1b[48;5;149m\x1b[38;5;149my\x1b[0m", "\x1b[1m\x1b[48;5;149m\x1b[38;5;149mx\x1b[0m\x1b[1m\x1b[48;5;149m\x1b[38;5;149my\x1b[0m", "\x1b[1m\x1b[48;5;236mx\x1b[0m\x1b[1m\x1b[48;5;236my\x1b[0m"},
	{2, 23, "\x1b[38;5;111mx\x1b[0m", "\x1b[48;5;111mx\x1b[0m\x1b[48;5;111my\x1b[0m", "\x1b[48;5;111m\x1b[38;5;111mx\x1b[0m\x1b[48;5;111m\x1b[38;5;111my\x1b[0m", "\x1b[1m\x1b[48;5;111m\x1b[38;5;111mx\x1b[0m\x1b[1m\x1b[48;5;111m\x1b[38;5;111my\x1b[0m", "\x1b[1m\x1b[48;5;236mx\x1b[0m\x1b[1m\x1b[48;5;236my\x1b[0m"},
	{2, 24, "\x1b[38;5;179mx\x1b[0m", "\x1b[48;5;179mx\x1b[0m\x1b[48;5;179my\x1b[0m", "\x1b[48;5;179m\x1b[38;5;179mx\x1b[0m\x1b[48;5;179m\x1b[38;5;179my\x1b[0m", "\x1b[1m\x1b[48;5;179m\x1b[38;5;179mx\x1b[0m\x1b[1m\x1b[48;5;179m\x1b[38;5;179my\x1b[0m", "\x1b[1m\x1b[48;5;236mx\x1b[0m\x1b[1m\x1b[48;5;236my\x1b[0m"},
	{2, 25, "\x1b[38;5;210mx\x1b[0m", "\x1b[48;5;210mx\x1b[0m\x1b[48;5;210my\x1b[0m", "\x1b[48;5;210m\x1b[38;5;210mx\x1b[0m\x1b[48;5;210m\x1b[38;5;210my\x1b[0m", "\x1b[1m\x1b[48;5;210m\x1b[38;5;210mx\x1b[0m\x1b[1m\x1b[48;5;210m\x1b[38;5;210my\x1b[0m", "\x1b[1m\x1b[48;5;236mx\x1b[0m\x1b[1m\x1b[48;5;236my\x1b[0m"},
	{2, 26, "\x1b[38;5;236mx\x1b[0m", "\x1b[48;5;236mx\x1b[0m\x1b[48;5;236my\x1b[0m", "\x1b[48;5;236m\x1b[38;5;236mx\x1b[0m\x1b[48;5;236m\x1b[38;5;236my\x1b[0m", "\x1b[1m\x1b[48;5;236m\x1b[38;5;236mx\x1b[0m\x1b[1m\x1b[48;5;236m\x1b[38;5;236my\x1b[0m", "\x1b[1m\x1b[48;5;236mx\x1b[0m\x1b[1m\x1b[48;5;236my\x1b[0m"},
	{2, 27, "\x1b[38;5;237mx\x1b[0m", "\x1b[48;5;237mx\x1b[0m\x1b[48;5;237my\x1b[0m", "\x1b[48;5;237m\x1b[38;5;237mx\x1b[0m\x1b[48;5;237m\x1b[38;5;237my\x1b[0m", "\x1b[1m\x1b[48;5;237m\x1b[38;5;237mx\x1b[0m\x1b[1m\x1b[48;5;237m\x1b[38;5;237my\x1b[0m", "\x1b[1m\x1b[48;5;236mx\x1b[0m\x1b[1m\x1b[48;5;236my\x1b[0m"},
	{2, 28, "\x1b[38;5;238mx\x1b[0m", "\x1b[48;5;238mx\x1b[0m\x1b[48;5;238my\x1b[0m", "\x1b[48;5;238m\x1b[38;5;238mx\x1b[0m\x1b[48;5;238m\x1b[38;5;238my\x1b[0m", "\x1b[1m\x1b[48;5;238m\x1b[38;5;238mx\x1b[0m\x1b[1m\x1b[48;5;238m\x1b[38;5;238my\x1b[0m", "\x1b[1m\x1b[48;5;236mx\x1b[0m\x1b[1m\x1b[48;5;236my\x1b[0m"},
	{2, 29, "\x1b[38;5;60mx\x1b[0m", "\x1b[48;5;60mx\x1b[0m\x1b[48;5;60my\x1b[0m", "\x1b[48;5;60m\x1b[38;5;60mx\x1b[0m\x1b[48;5;60m\x1b[38;5;60my\x1b[0m", "\x1b[1m\x1b[48;5;60m\x1b[38;5;60mx\x1b[0m\x1b[1m\x1b[48;5;60m\x1b[38;5;60my\x1b[0m", "\x1b[1m\x1b[48;5;236mx\x1b[0m\x1b[1m\x1b[48;5;236my\x1b[0m"},
	{2, 30, "\x1b[38;5;103mx\x1b[0m", "\x1b[48;5;103mx\x1b[0m\x1b[48;5;103my\x1b[0m", "\x1b[48;5;103m\x1b[38;5;103mx\x1b[0m\x1b[48;5;103m\x1b[38;5;103my\x1b[0m", "\x1b[1m\x1b[48;5;103m\x1b[38;5;103mx\x1b[0m\x1b[1m\x1b[48;5;103m\x1b[38;5;103my\x1b[0m", "\x1b[1m\x1b[48;5;236mx\x1b[0m\x1b[1m\x1b[48;5;236my\x1b[0m"},
	{2, 31, "\x1b[38;5;252mx\x1b[0m", "\x1b[48;5;252mx\x1b[0m\x1b[48;5;252my\x1b[0m", "\x1b[48;5;252m\x1b[38;5;252mx\x1b[0m\x1b[48;5;252m\x1b[38;5;252my\x1b[0m", "\x1b[1m\x1b[48;5;252m\x1b[38;5;252mx\x1b[0m\x1b[1m\x1b[48;5;252m\x1b[38;5;252my\x1b[0m", "\x1b[1m\x1b[48;5;236mx\x1b[0m\x1b[1m\x1b[48;5;236my\x1b[0m"},
	{2, 32, "\x1b[38;5;255mx\x1b[0m", "\x1b[48;5;255mx\x1b[0m\x1b[48;5;255my\x1b[0m", "\x1b[48;5;255m\x1b[38;5;255mx\x1b[0m\x1b[48;5;255m\x1b[38;5;255my\x1b[0m", "\x1b[1m\x1b[48;5;255m\x1b[38;5;255mx\x1b[0m\x1b[1m\x1b[48;5;255m\x1b[38;5;255my\x1b[0m", "\x1b[1m\x1b[48;5;236mx\x1b[0m\x1b[1m\x1b[48;5;236my\x1b[0m"},
	{2, 33, "\x1b[38;5;236mx\x1b[0m", "\x1b[48;5;236mx\x1b[0m\x1b[48;5;236my\x1b[0m", "\x1b[48;5;236m\x1b[38;5;236mx\x1b[0m\x1b[48;5;236m\x1b[38;5;236my\x1b[0m", "\x1b[1m\x1b[48;5;236m\x1b[38;5;236mx\x1b[0m\x1b[1m\x1b[48;5;236m\x1b[38;5;236my\x1b[0m", "\x1b[1m\x1b[48;5;236mx\x1b[0m\x1b[1m\x1b[48;5;236my\x1b[0m"},
	{3, 0, "\x1b[38;2;93;202;165mx\x1b[0m", "\x1b[48;2;93;202;165mx\x1b[0m\x1b[48;2;93;202;165my\x1b[0m", "\x1b[48;2;93;202;165m\x1b[38;2;93;202;165mx\x1b[0m\x1b[48;2;93;202;165m\x1b[38;2;93;202;165my\x1b[0m", "\x1b[1m\x1b[48;2;93;202;165m\x1b[38;2;93;202;165mx\x1b[0m\x1b[1m\x1b[48;2;93;202;165m\x1b[38;2;93;202;165my\x1b[0m", "\x1b[1m\x1b[48;2;35;42;66mx\x1b[0m\x1b[1m\x1b[48;2;35;42;66my\x1b[0m"},
	{3, 1, "\x1b[38;2;239;160;39mx\x1b[0m", "\x1b[48;2;239;160;39mx\x1b[0m\x1b[48;2;239;160;39my\x1b[0m", "\x1b[48;2;239;160;39m\x1b[38;2;239;160;39mx\x1b[0m\x1b[48;2;239;160;39m\x1b[38;2;239;160;39my\x1b[0m", "\x1b[1m\x1b[48;2;239;160;39m\x1b[38;2;239;160;39mx\x1b[0m\x1b[1m\x1b[48;2;239;160;39m\x1b[38;2;239;160;39my\x1b[0m", "\x1b[1m\x1b[48;2;35;42;66mx\x1b[0m\x1b[1m\x1b[48;2;35;42;66my\x1b[0m"},
	{3, 2, "\x1b[38;2;151;196;89mx\x1b[0m", "\x1b[48;2;151;196;89mx\x1b[0m\x1b[48;2;151;196;89my\x1b[0m", "\x1b[48;2;151;196;89m\x1b[38;2;151;196;89mx\x1b[0m\x1b[48;2;151;196;89m\x1b[38;2;151;196;89my\x1b[0m", "\x1b[1m\x1b[48;2;151;196;89m\x1b[38;2;151;196;89mx\x1b[0m\x1b[1m\x1b[48;2;151;196;89m\x1b[38;2;151;196;89my\x1b[0m", "\x1b[1m\x1b[48;2;35;42;66mx\x1b[0m\x1b[1m\x1b[48;2;35;42;66my\x1b[0m"},
	{3, 3, "\x1b[38;2;175;169;236mx\x1b[0m", "\x1b[48;2;175;169;236mx\x1b[0m\x1b[48;2;175;169;236my\x1b[0m", "\x1b[48;2;175;169;236m\x1b[38;2;175;169;236mx\x1b[0m\x1b[48;2;175;169;236m\x1b[38;2;175;169;236my\x1b[0m", "\x1b[1m\x1b[48;2;175;169;236m\x1b[38;2;175;169;236mx\x1b[0m\x1b[1m\x1b[48;2;175;169;236m\x1b[38;2;175;169;236my\x1b[0m", "\x1b[1m\x1b[48;2;35;42;66mx\x1b[0m\x1b[1m\x1b[48;2;35;42;66my\x1b[0m"},
	{3, 4, "\x1b[38;2;201;195;247mx\x1b[0m", "\x1b[48;2;201;195;247mx\x1b[0m\x1b[48;2;201;195;247my\x1b[0m", "\x1b[48;2;201;195;247m\x1b[38;2;201;195;247mx\x1b[0m\x1b[48;2;201;195;247m\x1b[38;2;201;195;247my\x1b[0m", "\x1b[1m\x1b[48;2;201;195;247m\x1b[38;2;201;195;247mx\x1b[0m\x1b[1m\x1b[48;2;201;195;247m\x1b[38;2;201;195;247my\x1b[0m", "\x1b[1m\x1b[48;2;35;42;66mx\x1b[0m\x1b[1m\x1b[48;2;35;42;66my\x1b[0m"},
	{3, 5, "\x1b[38;2;93;202;165mx\x1b[0m", "\x1b[48;2;93;202;165mx\x1b[0m\x1b[48;2;93;202;165my\x1b[0m", "\x1b[48;2;93;202;165m\x1b[38;2;93;202;165mx\x1b[0m\x1b[48;2;93;202;165m\x1b[38;2;93;202;165my\x1b[0m", "\x1b[1m\x1b[48;2;93;202;165m\x1b[38;2;93;202;165mx\x1b[0m\x1b[1m\x1b[48;2;93;202;165m\x1b[38;2;93;202;165my\x1b[0m", "\x1b[1m\x1b[48;2;35;42;66mx\x1b[0m\x1b[1m\x1b[48;2;35;42;66my\x1b[0m"},
	{3, 6, "\x1b[38;2;158;206;106mx\x1b[0m", "\x1b[48;2;158;206;106mx\x1b[0m\x1b[48;2;158;206;106my\x1b[0m", "\x1b[48;2;158;206;106m\x1b[38;2;158;206;106mx\x1b[0m\x1b[48;2;158;206;106m\x1b[38;2;158;206;106my\x1b[0m", "\x1b[1m\x1b[48;2;158;206;106m\x1b[38;2;158;206;106mx\x1b[0m\x1b[1m\x1b[48;2;158;206;106m\x1b[38;2;158;206;106my\x1b[0m", "\x1b[1m\x1b[48;2;35;42;66mx\x1b[0m\x1b[1m\x1b[48;2;35;42;66my\x1b[0m"},
	{3, 7, "\x1b[38;2;122;162;247mx\x1b[0m", "\x1b[48;2;122;162;247mx\x1b[0m\x1b[48;2;122;162;247my\x1b[0m", "\x1b[48;2;122;162;247m\x1b[38;2;122;162;247mx\x1b[0m\x1b[48;2;122;162;247m\x1b[38;2;122;162;247my\x1b[0m", "\x1b[1m\x1b[48;2;122;162;247m\x1b[38;2;122;162;247mx\x1b[0m\x1b[1m\x1b[48;2;122;162;247m\x1b[38;2;122;162;247my\x1b[0m", "\x1b[1m\x1b[48;2;35;42;66mx\x1b[0m\x1b[1m\x1b[48;2;35;42;66my\x1b[0m"},
	{3, 8, "\x1b[38;2;86;95;137mx\x1b[0m", "\x1b[48;2;86;95;137mx\x1b[0m\x1b[48;2;86;95;137my\x1b[0m", "\x1b[48;2;86;95;137m\x1b[38;2;86;95;137mx\x1b[0m\x1b[48;2;86;95;137m\x1b[38;2;86;95;137my\x1b[0m", "\x1b[1m\x1b[48;2;86;95;137m\x1b[38;2;86;95;137mx\x1b[0m\x1b[1m\x1b[48;2;86;95;137m\x1b[38;2;86;95;137my\x1b[0m", "\x1b[1m\x1b[48;2;35;42;66mx\x1b[0m\x1b[1m\x1b[48;2;35;42;66my\x1b[0m"},
	{3, 9, "\x1b[38;2;26;27;38mx\x1b[0m", "\x1b[48;2;26;27;38mx\x1b[0m\x1b[48;2;26;27;38my\x1b[0m", "\x1b[48;2;26;27;38m\x1b[38;2;26;27;38mx\x1b[0m\x1b[48;2;26;27;38m\x1b[38;2;26;27;38my\x1b[0m", "\x1b[1m\x1b[48;2;26;27;38m\x1b[38;2;26;27;38mx\x1b[0m\x1b[1m\x1b[48;2;26;27;38m\x1b[38;2;26;27;38my\x1b[0m", "\x1b[1m\x1b[48;2;35;42;66mx\x1b[0m\x1b[1m\x1b[48;2;35;42;66my\x1b[0m"},
	{3, 10, "\x1b[38;2;23;28;44mx\x1b[0m", "\x1b[48;2;23;28;44mx\x1b[0m\x1b[48;2;23;28;44my\x1b[0m", "\x1b[48;2;23;28;44m\x1b[38;2;23;28;44mx\x1b[0m\x1b[48;2;23;28;44m\x1b[38;2;23;28;44my\x1b[0m", "\x1b[1m\x1b[48;2;23;28;44m\x1b[38;2;23;28;44mx\x1b[0m\x1b[1m\x1b[48;2;23;28;44m\x1b[38;2;23;28;44my\x1b[0m", "\x1b[1m\x1b[48;2;35;42;66mx\x1b[0m\x1b[1m\x1b[48;2;35;42;66my\x1b[0m"},
	{3, 11, "\x1b[38;2;22;24;33mx\x1b[0m", "\x1b[48;2;22;24;33mx\x1b[0m\x1b[48;2;22;24;33my\x1b[0m", "\x1b[48;2;22;24;33m\x1b[38;2;22;24;33mx\x1b[0m\x1b[48;2;22;24;33m\x1b[38;2;22;24;33my\x1b[0m", "\x1b[1m\x1b[48;2;22;24;33m\x1b[38;2;22;24;33mx\x1b[0m\x1b[1m\x1b[48;2;22;24;33m\x1b[38;2;22;24;33my\x1b[0m", "\x1b[1m\x1b[48;2;35;42;66mx\x1b[0m\x1b[1m\x1b[48;2;35;42;66my\x1b[0m"},
	{3, 12, "\x1b[38;2;115;122;162mx\x1b[0m", "\x1b[48;2;115;122;162mx\x1b[0m\x1b[48;2;115;122;162my\x1b[0m", "\x1b[48;2;115;122;162m\x1b[38;2;115;122;162mx\x1b[0m\x1b[48;2;115;122;162m\x1b[38;2;115;122;162my\x1b[0m", "\x1b[1m\x1b[48;2;115;122;162m\x1b[38;2;115;122;162mx\x1b[0m\x1b[1m\x1b[48;2;115;122;162m\x1b[38;2;115;122;162my\x1b[0m", "\x1b[1m\x1b[48;2;35;42;66mx\x1b[0m\x1b[1m\x1b[48;2;35;42;66my\x1b[0m"},
	{3, 13, "\x1b[38;2;40;52;87mx\x1b[0m", "\x1b[48;2;40;52;87mx\x1b[0m\x1b[48;2;40;52;87my\x1b[0m", "\x1b[48;2;40;52;87m\x1b[38;2;40;52;87mx\x1b[0m\x1b[48;2;40;52;87m\x1b[38;2;40;52;87my\x1b[0m", "\x1b[1m\x1b[48;2;40;52;87m\x1b[38;2;40;52;87mx\x1b[0m\x1b[1m\x1b[48;2;40;52;87m\x1b[38;2;40;52;87my\x1b[0m", "\x1b[1m\x1b[48;2;35;42;66mx\x1b[0m\x1b[1m\x1b[48;2;35;42;66my\x1b[0m"},
	{3, 14, "\x1b[38;2;192;202;245mx\x1b[0m", "\x1b[48;2;192;202;245mx\x1b[0m\x1b[48;2;192;202;245my\x1b[0m", "\x1b[48;2;192;202;245m\x1b[38;2;192;202;245mx\x1b[0m\x1b[48;2;192;202;245m\x1b[38;2;192;202;245my\x1b[0m", "\x1b[1m\x1b[48;2;192;202;245m\x1b[38;2;192;202;245mx\x1b[0m\x1b[1m\x1b[48;2;192;202;245m\x1b[38;2;192;202;245my\x1b[0m", "\x1b[1m\x1b[48;2;35;42;66mx\x1b[0m\x1b[1m\x1b[48;2;35;42;66my\x1b[0m"},
	{3, 15, "\x1b[38;2;86;95;137mx\x1b[0m", "\x1b[48;2;86;95;137mx\x1b[0m\x1b[48;2;86;95;137my\x1b[0m", "\x1b[48;2;86;95;137m\x1b[38;2;86;95;137mx\x1b[0m\x1b[48;2;86;95;137m\x1b[38;2;86;95;137my\x1b[0m", "\x1b[1m\x1b[48;2;86;95;137m\x1b[38;2;86;95;137mx\x1b[0m\x1b[1m\x1b[48;2;86;95;137m\x1b[38;2;86;95;137my\x1b[0m", "\x1b[1m\x1b[48;2;35;42;66mx\x1b[0m\x1b[1m\x1b[48;2;35;42;66my\x1b[0m"},
	{3, 16, "\x1b[38;2;86;95;137mx\x1b[0m", "\x1b[48;2;86;95;137mx\x1b[0m\x1b[48;2;86;95;137my\x1b[0m", "\x1b[48;2;86;95;137m\x1b[38;2;86;95;137mx\x1b[0m\x1b[48;2;86;95;137m\x1b[38;2;86;95;137my\x1b[0m", "\x1b[1m\x1b[48;2;86;95;137m\x1b[38;2;86;95;137mx\x1b[0m\x1b[1m\x1b[48;2;86;95;137m\x1b[38;2;86;95;137my\x1b[0m", "\x1b[1m\x1b[48;2;35;42;66mx\x1b[0m\x1b[1m\x1b[48;2;35;42;66my\x1b[0m"},
	{3, 17, "\x1b[38;2;26;27;38mx\x1b[0m", "\x1b[48;2;26;27;38mx\x1b[0m\x1b[48;2;26;27;38my\x1b[0m", "\x1b[48;2;26;27;38m\x1b[38;2;26;27;38mx\x1b[0m\x1b[48;2;26;27;38m\x1b[38;2;26;27;38my\x1b[0m", "\x1b[1m\x1b[48;2;26;27;38m\x1b[38;2;26;27;38mx\x1b[0m\x1b[1m\x1b[48;2;26;27;38m\x1b[38;2;26;27;38my\x1b[0m", "\x1b[1m\x1b[48;2;35;42;66mx\x1b[0m\x1b[1m\x1b[48;2;35;42;66my\x1b[0m"},
	{3, 18, "\x1b[38;2;192;202;245mx\x1b[0m", "\x1b[48;2;192;202;245mx\x1b[0m\x1b[48;2;192;202;245my\x1b[0m", "\x1b[48;2;192;202;245m\x1b[38;2;192;202;245mx\x1b[0m\x1b[48;2;192;202;245m\x1b[38;2;192;202;245my\x1b[0m", "\x1b[1m\x1b[48;2;192;202;245m\x1b[38;2;192;202;245mx\x1b[0m\x1b[1m\x1b[48;2;192;202;245m\x1b[38;2;192;202;245my\x1b[0m", "\x1b[1m\x1b[48;2;35;42;66mx\x1b[0m\x1b[1m\x1b[48;2;35;42;66my\x1b[0m"},
	{3, 19, "\x1b[38;2;169;177;214mx\x1b[0m", "\x1b[48;2;169;177;214mx\x1b[0m\x1b[48;2;169;177;214my\x1b[0m", "\x1b[48;2;169;177;214m\x1b[38;2;169;177;214mx\x1b[0m\x1b[48;2;169;177;214m\x1b[38;2;169;177;214my\x1b[0m", "\x1b[1m\x1b[48;2;169;177;214m\x1b[38;2;169;177;214mx\x1b[0m\x1b[1m\x1b[48;2;169;177;214m\x1b[38;2;169;177;214my\x1b[0m", "\x1b[1m\x1b[48;2;35;42;66mx\x1b[0m\x1b[1m\x1b[48;2;35;42;66my\x1b[0m"},
	{3, 20, "\x1b[38;2;86;95;137mx\x1b[0m", "\x1b[48;2;86;95;137mx\x1b[0m\x1b[48;2;86;95;137my\x1b[0m", "\x1b[48;2;86;95;137m\x1b[38;2;86;95;137mx\x1b[0m\x1b[48;2;86;95;137m\x1b[38;2;86;95;137my\x1b[0m", "\x1b[1m\x1b[48;2;86;95;137m\x1b[38;2;86;95;137mx\x1b[0m\x1b[1m\x1b[48;2;86;95;137m\x1b[38;2;86;95;137my\x1b[0m", "\x1b[1m\x1b[48;2;35;42;66mx\x1b[0m\x1b[1m\x1b[48;2;35;42;66my\x1b[0m"},
	{3, 21, "\x1b[38;2;115;122;162mx\x1b[0m", "\x1b[48;2;115;122;162mx\x1b[0m\x1b[48;2;115;122;162my\x1b[0m", "\x1b[48;2;115;122;162m\x1b[38;2;115;122;162mx\x1b[0m\x1b[48;2;115;122;162m\x1b[38;2;115;122;162my\x1b[0m", "\x1b[1m\x1b[48;2;115;122;162m\x1b[38;2;115;122;162mx\x1b[0m\x1b[1m\x1b[48;2;115;122;162m\x1b[38;2;115;122;162my\x1b[0m", "\x1b[1m\x1b[48;2;35;42;66mx\x1b[0m\x1b[1m\x1b[48;2;35;42;66my\x1b[0m"},
	{3, 22, "\x1b[38;2;158;206;106mx\x1b[0m", "\x1b[48;2;158;206;106mx\x1b[0m\x1b[48;2;158;206;106my\x1b[0m", "\x1b[48;2;158;206;106m\x1b[38;2;158;206;106mx\x1b[0m\x1b[48;2;158;206;106m\x1b[38;2;158;206;106my\x1b[0m", "\x1b[1m\x1b[48;2;158;206;106m\x1b[38;2;158;206;106mx\x1b[0m\x1b[1m\x1b[48;2;158;206;106m\x1b[38;2;158;206;106my\x1b[0m", "\x1b[1m\x1b[48;2;35;42;66mx\x1b[0m\x1b[1m\x1b[48;2;35;42;66my\x1b[0m"},
	{3, 23, "\x1b[38;2;122;162;247mx\x1b[0m", "\x1b[48;2;122;162;247mx\x1b[0m\x1b[48;2;122;162;247my\x1b[0m", "\x1b[48;2;122;162;247m\x1b[38;2;122;162;247mx\x1b[0m\x1b[48;2;122;162;247m\x1b[38;2;122;162;247my\x1b[0m", "\x1b[1m\x1b[48;2;122;162;247m\x1b[38;2;122;162;247mx\x1b[0m\x1b[1m\x1b[48;2;122;162;247m\x1b[38;2;122;162;247my\x1b[0m", "\x1b[1m\x1b[48;2;35;42;66mx\x1b[0m\x1b[1m\x1b[48;2;35;42;66my\x1b[0m"},
	{3, 24, "\x1b[38;2;224;175;104mx\x1b[0m", "\x1b[48;2;224;175;104mx\x1b[0m\x1b[48;2;224;175;104my\x1b[0m", "\x1b[48;2;224;175;104m\x1b[38;2;224;175;104mx\x1b[0m\x1b[48;2;224;175;104m\x1b[38;2;224;175;104my\x1b[0m", "\x1b[1m\x1b[48;2;224;175;104m\x1b[38;2;224;175;104mx\x1b[0m\x1b[1m\x1b[48;2;224;175;104m\x1b[38;2;224;175;104my\x1b[0m", "\x1b[1m\x1b[48;2;35;42;66mx\x1b[0m\x1b[1m\x1b[48;2;35;42;66my\x1b[0m"},
	{3, 25, "\x1b[38;2;247;118;142mx\x1b[0m", "\x1b[48;2;247;118;142mx\x1b[0m\x1b[48;2;247;118;142my\x1b[0m", "\x1b[48;2;247;118;142m\x1b[38;2;247;118;142mx\x1b[0m\x1b[48;2;247;118;142m\x1b[38;2;247;118;142my\x1b[0m", "\x1b[1m\x1b[48;2;247;118;142m\x1b[38;2;247;118;142mx\x1b[0m\x1b[1m\x1b[48;2;247;118;142m\x1b[38;2;247;118;142my\x1b[0m", "\x1b[1m\x1b[48;2;35;42;66mx\x1b[0m\x1b[1m\x1b[48;2;35;42;66my\x1b[0m"},
	{3, 26, "\x1b[38;2;41;46;66mx\x1b[0m", "\x1b[48;2;41;46;66mx\x1b[0m\x1b[48;2;41;46;66my\x1b[0m", "\x1b[48;2;41;46;66m\x1b[38;2;41;46;66mx\x1b[0m\x1b[48;2;41;46;66m\x1b[38;2;41;46;66my\x1b[0m", "\x1b[1m\x1b[48;2;41;46;66m\x1b[38;2;41;46;66mx\x1b[0m\x1b[1m\x1b[48;2;41;46;66m\x1b[38;2;41;46;66my\x1b[0m", "\x1b[1m\x1b[48;2;35;42;66mx\x1b[0m\x1b[1m\x1b[48;2;35;42;66my\x1b[0m"},
	{3, 27, "\x1b[38;2;44;49;69mx\x1b[0m", "\x1b[48;2;44;49;69mx\x1b[0m\x1b[48;2;44;49;69my\x1b[0m", "\x1b[48;2;44;49;69m\x1b[38;2;44;49;69mx\x1b[0m\x1b[48;2;44;49;69m\x1b[38;2;44;49;69my\x1b[0m", "\x1b[1m\x1b[48;2;44;49;69m\x1b[38;2;44;49;69mx\x1b[0m\x1b[1m\x1b[48;2;44;49;69m\x1b[38;2;44;49;69my\x1b[0m", "\x1b[1m\x1b[48;2;35;42;66mx\x1b[0m\x1b[1m\x1b[48;2;35;42;66my\x1b[0m"},
	{3, 28, "\x1b[38;2;61;66;87mx\x1b[0m", "\x1b[48;2;61;66;87mx\x1b[0m\x1b[48;2;61;66;87my\x1b[0m", "\x1b[48;2;61;66;87m\x1b[38;2;61;66;87mx\x1b[0m\x1b[48;2;61;66;87m\x1b[38;2;61;66;87my\x1b[0m", "\x1b[1m\x1b[48;2;61;66;87m\x1b[38;2;61;66;87mx\x1b[0m\x1b[1m\x1b[48;2;61;66;87m\x1b[38;2;61;66;87my\x1b[0m", "\x1b[1m\x1b[48;2;35;42;66mx\x1b[0m\x1b[1m\x1b[48;2;35;42;66my\x1b[0m"},
	{3, 29, "\x1b[38;2;107;113;145mx\x1b[0m", "\x1b[48;2;107;113;145mx\x1b[0m\x1b[48;2;107;113;145my\x1b[0m", "\x1b[48;2;107;113;145m\x1b[38;2;107;113;145mx\x1b[0m\x1b[48;2;107;113;145m\x1b[38;2;107;113;145my\x1b[0m", "\x1b[1m\x1b[48;2;107;113;145m\x1b[38;2;107;113;145mx\x1b[0m\x1b[1m\x1b[48;2;107;113;145m\x1b[38;2;107;113;145my\x1b[0m", "\x1b[1m\x1b[48;2;35;42;66mx\x1b[0m\x1b[1m\x1b[48;2;35;42;66my\x1b[0m"},
	{3, 30, "\x1b[38;2;139;144;171mx\x1b[0m", "\x1b[48;2;139;144;171mx\x1b[0m\x1b[48;2;139;144;171my\x1b[0m", "\x1b[48;2;139;144;171m\x1b[38;2;139;144;171mx\x1b[0m\x1b[48;2;139;144;171m\x1b[38;2;139;144;171my\x1b[0m", "\x1b[1m\x1b[48;2;139;144;171m\x1b[38;2;139;144;171mx\x1b[0m\x1b[1m\x1b[48;2;139;144;171m\x1b[38;2;139;144;171my\x1b[0m", "\x1b[1m\x1b[48;2;35;42;66mx\x1b[0m\x1b[1m\x1b[48;2;35;42;66my\x1b[0m"},
	{3, 31, "\x1b[38;2;200;202;216mx\x1b[0m", "\x1b[48;2;200;202;216mx\x1b[0m\x1b[48;2;200;202;216my\x1b[0m", "\x1b[48;2;200;202;216m\x1b[38;2;200;202;216mx\x1b[0m\x1b[48;2;200;202;216m\x1b[38;2;200;202;216my\x1b[0m", "\x1b[1m\x1b[48;2;200;202;216m\x1b[38;2;200;202;216mx\x1b[0m\x1b[1m\x1b[48;2;200;202;216m\x1b[38;2;200;202;216my\x1b[0m", "\x1b[1m\x1b[48;2;35;42;66mx\x1b[0m\x1b[1m\x1b[48;2;35;42;66my\x1b[0m"},
	{3, 32, "\x1b[38;2;230;232;242mx\x1b[0m", "\x1b[48;2;230;232;242mx\x1b[0m\x1b[48;2;230;232;242my\x1b[0m", "\x1b[48;2;230;232;242m\x1b[38;2;230;232;242mx\x1b[0m\x1b[48;2;230;232;242m\x1b[38;2;230;232;242my\x1b[0m", "\x1b[1m\x1b[48;2;230;232;242m\x1b[38;2;230;232;242mx\x1b[0m\x1b[1m\x1b[48;2;230;232;242m\x1b[38;2;230;232;242my\x1b[0m", "\x1b[1m\x1b[48;2;35;42;66mx\x1b[0m\x1b[1m\x1b[48;2;35;42;66my\x1b[0m"},
	{3, 33, "\x1b[38;2;35;42;66mx\x1b[0m", "\x1b[48;2;35;42;66mx\x1b[0m\x1b[48;2;35;42;66my\x1b[0m", "\x1b[48;2;35;42;66m\x1b[38;2;35;42;66mx\x1b[0m\x1b[48;2;35;42;66m\x1b[38;2;35;42;66my\x1b[0m", "\x1b[1m\x1b[48;2;35;42;66m\x1b[38;2;35;42;66mx\x1b[0m\x1b[1m\x1b[48;2;35;42;66m\x1b[38;2;35;42;66my\x1b[0m", "\x1b[1m\x1b[48;2;35;42;66mx\x1b[0m\x1b[1m\x1b[48;2;35;42;66my\x1b[0m"},
}

func TestSemanticPaletteUsesApprovedColors(t *testing.T) {
	tests := []struct {
		name string
		got  Color
		hex  string
	}{
		{name: "accent", got: Default().Palette(ProfileTrueColor).MustColor(RoleAccent), hex: "#5dcaa5"},
		{name: "warn", got: Default().Palette(ProfileTrueColor).MustColor(RoleWarn), hex: "#efa027"},
		{name: "success", got: Default().Palette(ProfileTrueColor).MustColor(RoleSuccess), hex: "#97c459"},
		{name: "plan now", got: Default().Palette(ProfileTrueColor).MustColor(RolePlanNow), hex: "#9ECE6A"},
		{name: "plan next", got: Default().Palette(ProfileTrueColor).MustColor(RolePlanNext), hex: "#7AA2F7"},
		{name: "plan history", got: Default().Palette(ProfileTrueColor).MustColor(RolePlanHistory), hex: "#565F89"},
		{name: "plan now background", got: Default().Palette(ProfileTrueColor).MustColor(RolePlanNowBackground), hex: "#1A1B26"},
		{name: "plan next background", got: Default().Palette(ProfileTrueColor).MustColor(RolePlanNextBackground), hex: "#171C2C"},
		{name: "plan history background", got: Default().Palette(ProfileTrueColor).MustColor(RolePlanHistoryBackground), hex: "#161821"},
		{name: "plan history text", got: Default().Palette(ProfileTrueColor).MustColor(RolePlanHistoryText), hex: "#737AA2"},
		{name: "plan selection background", got: Default().Palette(ProfileTrueColor).MustColor(RolePlanSelectionBackground), hex: "#283457"},
		{name: "plan selection text", got: Default().Palette(ProfileTrueColor).MustColor(RolePlanSelectionText), hex: "#C0CAF5"},
		{name: "settings section", got: Default().Palette(ProfileTrueColor).MustColor(RoleSettingsSection), hex: "#565F89"},
		{name: "debug section", got: Default().Palette(ProfileTrueColor).MustColor(RoleDebugSection), hex: "#565F89"},
		{name: "detail background", got: Default().Palette(ProfileTrueColor).MustColor(RoleDetailBackground), hex: "#1A1B26"},
		{name: "detail primary", got: Default().Palette(ProfileTrueColor).MustColor(RoleDetailPrimary), hex: "#C0CAF5"},
		{name: "detail secondary", got: Default().Palette(ProfileTrueColor).MustColor(RoleDetailSecondary), hex: "#A9B1D6"},
		{name: "detail muted", got: Default().Palette(ProfileTrueColor).MustColor(RoleDetailMuted), hex: "#565F89"},
		{name: "detail body", got: Default().Palette(ProfileTrueColor).MustColor(RoleDetailBody), hex: "#737AA2"},
		{name: "detail success", got: Default().Palette(ProfileTrueColor).MustColor(RoleDetailSuccess), hex: "#9ECE6A"},
		{name: "detail info", got: Default().Palette(ProfileTrueColor).MustColor(RoleDetailInfo), hex: "#7AA2F7"},
		{name: "detail warning", got: Default().Palette(ProfileTrueColor).MustColor(RoleDetailWarning), hex: "#E0AF68"},
		{name: "detail error", got: Default().Palette(ProfileTrueColor).MustColor(RoleDetailError), hex: "#F7768E"},
		{name: "detail divider", got: Default().Palette(ProfileTrueColor).MustColor(RoleDetailDivider), hex: "#292E42"},
		{name: "repo", got: Default().Palette(ProfileTrueColor).MustColor(RoleRepo), hex: "#afa9ec"},
		{name: "repo selected", got: Default().Palette(ProfileTrueColor).MustColor(RoleRepoSelected), hex: "#c9c3f7"},
		{name: "n0", got: Default().Palette(ProfileTrueColor).MustColor(RoleNeutral0), hex: "#2c3145"},
		{name: "n1", got: Default().Palette(ProfileTrueColor).MustColor(RoleNeutral1), hex: "#3d4257"},
		{name: "n2", got: Default().Palette(ProfileTrueColor).MustColor(RoleNeutral2), hex: "#6b7191"},
		{name: "n3", got: Default().Palette(ProfileTrueColor).MustColor(RoleNeutral3), hex: "#8b90ab"},
		{name: "n4", got: Default().Palette(ProfileTrueColor).MustColor(RoleNeutral4), hex: "#c8cad8"},
		{name: "n5", got: Default().Palette(ProfileTrueColor).MustColor(RoleNeutral5), hex: "#e6e8f2"},
		{name: "selection background", got: Default().Palette(ProfileTrueColor).MustColor(RoleSelectionBackground), hex: "#232a42"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			want, err := ProfileTrueColor.Convert(test.hex)
			if err != nil {
				t.Fatal(err)
			}
			if test.got != want {
				t.Fatalf("color = %#v, want %#v", test.got, want)
			}
		})
	}

	if info, assigned := Default().Palette(ProfileTrueColor).Color(RoleInfo); !assigned || info != Default().Palette(ProfileTrueColor).MustColor(RoleAccent) {
		t.Fatalf("info role = %#v, assigned %t; want assigned accent hue", info, assigned)
	}
}

func TestSemanticHuesRemainDistinctInANSI16(t *testing.T) {
	hues := []Color{
		Default().Palette(ProfileANSI16).MustColor(RoleAccent),
		Default().Palette(ProfileANSI16).MustColor(RoleWarn),
		Default().Palette(ProfileANSI16).MustColor(RoleSuccess),
		Default().Palette(ProfileANSI16).MustColor(RoleRepo),
	}
	for index, hue := range hues {
		for other := index + 1; other < len(hues); other++ {
			if hue.Index == hues[other].Index {
				t.Fatalf("hues %d and %d both degrade to ANSI color %d", index, other, hue.Index)
			}
		}
	}

	const defaultBackgroundIndex = 0
	if got := Default().Palette(ProfileANSI16).MustColor(RoleSelectionBackground).Index; got == defaultBackgroundIndex {
		t.Fatalf("selection background degrades to default background index %d", got)
	}
}

func TestRepoColorIsStableFixedPaletteAssignment(t *testing.T) {
	id := "repo-073fb4994a1a"
	first := RepoColor(id)
	if first != RoleWarn {
		t.Fatalf("RepoColor(%q) = %d, want stable FNV-1a slot %d", id, first, RoleWarn)
	}
	if second := RepoColor(id); second != first {
		t.Fatalf("RepoColor(%q) changed from %d to %d", id, first, second)
	}

	valid := false
	for _, role := range repoColorRoles {
		if first == role {
			valid = true
			break
		}
	}
	if !valid {
		t.Fatalf("RepoColor(%q) = %d, outside fixed palette", id, first)
	}
}

func TestSelectRowPreservesForegroundWithoutReverseVideo(t *testing.T) {
	foreground := Default().Palette(ProfileTrueColor).Paint(RoleWarn, "warning")
	got := Default().Palette(ProfileTrueColor).SelectRow(foreground + " tail")
	wantForeground := Sequence(Default().Palette(ProfileTrueColor).MustColor(RoleWarn), false)
	wantBackground := Sequence(Default().Palette(ProfileTrueColor).MustColor(RoleSelectionBackground), true)
	if !strings.Contains(got, wantForeground) {
		t.Fatalf("selected row lost foreground sequence: %q", got)
	}
	if strings.Count(got, wantBackground) < 2 {
		t.Fatalf("selected row did not restore background after cell reset: %q", got)
	}
	if strings.Contains(got, "\x1b[7m") {
		t.Fatalf("selected row uses reverse video: %q", got)
	}
	if !strings.Contains(got, Bold) {
		t.Fatalf("selected row does not brighten foreground: %q", got)
	}
	if plain := Default().Palette(ProfileNone).SelectRow("plain"); plain != "plain" {
		t.Fatalf("no-color selected row = %q", plain)
	}
}
