package theme

import (
	"fmt"
	"strings"
)

// Theme is an immutable semantic palette. Its zero value is the default theme.
type Theme struct {
	name  string
	specs [roleCount]roleSpec
}

type roleSpec struct {
	hex    string
	ansi16 uint8
}

func (t Theme) resolved() Theme {
	if t.name == "" {
		return Default()
	}
	return t
}

func (t Theme) Name() string { return t.resolved().name }

func Default() Theme { return tokyonight }

// Lookup resolves a canonical theme name or the alias default, ignoring case
// and surrounding whitespace. Unknown names return false.
func Lookup(name string) (Theme, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "default", tokyonight.name:
		return Default(), true
	case gruvbox.name:
		return gruvbox, true
	default:
		return Theme{}, false
	}
}

// Names returns canonical theme names in stable order, with the default first.
func Names() []string {
	return []string{tokyonight.name, gruvbox.name}
}

// Palette binds a theme to a terminal's color resolution.
type Palette struct {
	Theme   Theme
	Profile Profile
}

func (t Theme) Palette(profile Profile) Palette {
	return Palette{Theme: t.resolved(), Profile: profile}
}

func (p Palette) Enabled() bool { return p.Profile.Enabled() }

func (p Palette) Color(role Role) (Color, bool) {
	if role >= roleCount {
		return Color{}, false
	}
	spec := p.Theme.resolved().specs[role]
	if spec.hex == "" {
		return Color{}, false
	}
	color, err := p.Profile.Convert(spec.hex)
	if err != nil {
		panic(err) // Palette literals are compile-time-owned values.
	}
	if p.Profile == ProfileANSI16 {
		color.Index = spec.ansi16
	}
	return color, true
}

// Paint applies a semantic foreground role to text.
func (p Palette) Paint(role Role, text string) string {
	color, ok := p.Color(role)
	if !ok || p.Profile == ProfileNone || text == "" {
		return text
	}
	return Sequence(color, false) + text + Reset
}

// FillRow preserves foreground sequences and restores the background after resets.
func (p Palette) FillRow(background Role, row string) string {
	if p.Profile == ProfileNone || row == "" {
		return row
	}
	prefix := Sequence(p.MustColor(background), true)
	row = strings.ReplaceAll(row, Reset, Reset+prefix)
	return prefix + row + Reset
}

func (p Palette) FillRowWithText(foreground, background Role, bold bool, row string) string {
	if p.Profile == ProfileNone || row == "" {
		return row
	}
	prefix := Sequence(p.MustColor(background), true) + Sequence(p.MustColor(foreground), false)
	if bold {
		prefix = Bold + prefix
	}
	row = strings.ReplaceAll(row, Reset, Reset+prefix)
	return prefix + row + Reset
}

// SelectRow applies bold and the selection background, restoring both after resets.
func (p Palette) SelectRow(row string) string {
	if p.Profile == ProfileNone || row == "" {
		return row
	}
	prefix := Bold + Sequence(p.MustColor(RoleSelectionBackground), true)
	row = strings.ReplaceAll(row, Reset, Reset+prefix)
	return prefix + row + Reset
}

func (p Palette) MustColor(role Role) Color {
	color, ok := p.Color(role)
	if !ok {
		panic(fmt.Sprintf("unassigned color role %d", role))
	}
	return color
}

const (
	Reset = "\x1b[0m"
	Bold  = "\x1b[1m"
)

func Sequence(color Color, background bool) string {
	if color.Profile == ProfileNone {
		return ""
	}
	base := 30
	if background {
		base = 40
	}
	switch color.Profile {
	case ProfileANSI16:
		if color.Index < 8 {
			return fmt.Sprintf("\x1b[%dm", base+int(color.Index))
		}
		return fmt.Sprintf("\x1b[%dm", base+60+int(color.Index-8))
	case ProfileANSI256:
		return fmt.Sprintf("\x1b[%d;5;%dm", base+8, color.Index)
	case ProfileTrueColor:
		return fmt.Sprintf("\x1b[%d;2;%d;%d;%dm", base+8, color.Red, color.Green, color.Blue)
	default:
		return ""
	}
}
