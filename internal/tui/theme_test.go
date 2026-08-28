package tui

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestDetectProfileEnvironmentPrecedence(t *testing.T) {
	tests := []struct {
		name        string
		isTerminal  bool
		environment map[string]string
		want        Profile
	}{
		{
			name:       "NO_COLOR wins over forced truecolor",
			isTerminal: true,
			environment: map[string]string{
				"NO_COLOR":       "1",
				"CLICOLOR_FORCE": "1",
				"COLORTERM":      "truecolor",
				"TERM":           "xterm-256color",
			},
			want: ProfileNone,
		},
		{
			name:       "dumb terminal wins over force",
			isTerminal: true,
			environment: map[string]string{
				"CLICOLOR_FORCE": "1",
				"COLORTERM":      "truecolor",
				"TERM":           "dumb",
			},
			want: ProfileNone,
		},
		{
			name:       "force enables redirected output",
			isTerminal: false,
			environment: map[string]string{
				"CLICOLOR_FORCE": "1",
				"COLORTERM":      "truecolor",
			},
			want: ProfileTrueColor,
		},
		{
			name:       "force wins over CLICOLOR zero",
			isTerminal: false,
			environment: map[string]string{
				"CLICOLOR":       "0",
				"CLICOLOR_FORCE": "1",
				"TERM":           "xterm-256color",
			},
			want: ProfileANSI256,
		},
		{
			name:       "zero force does not enable redirected output",
			isTerminal: false,
			environment: map[string]string{
				"CLICOLOR_FORCE": "0",
				"COLORTERM":      "truecolor",
			},
			want: ProfileNone,
		},
		{
			name:       "CLICOLOR zero disables terminal color",
			isTerminal: true,
			environment: map[string]string{
				"CLICOLOR":  "0",
				"COLORTERM": "truecolor",
				"TERM":      "xterm-256color",
			},
			want: ProfileNone,
		},
		{
			name:       "CLICOLOR does not color redirected output",
			isTerminal: false,
			environment: map[string]string{
				"CLICOLOR": "1",
				"TERM":     "xterm-256color",
			},
			want: ProfileNone,
		},
		{
			name:       "COLORTERM truecolor",
			isTerminal: true,
			environment: map[string]string{
				"COLORTERM": "truecolor",
			},
			want: ProfileTrueColor,
		},
		{
			name:       "COLORTERM 24bit marker",
			isTerminal: true,
			environment: map[string]string{
				"COLORTERM": "terminal-24bit",
			},
			want: ProfileTrueColor,
		},
		{
			name:       "TERM 256color",
			isTerminal: true,
			environment: map[string]string{
				"TERM": "screen-256color",
			},
			want: ProfileANSI256,
		},
		{
			name:       "ordinary terminal falls back to 16 colors",
			isTerminal: true,
			environment: map[string]string{
				"TERM": "xterm",
			},
			want: ProfileANSI16,
		},
		{
			name:        "redirected output has no color",
			isTerminal:  false,
			environment: map[string]string{"TERM": "xterm-256color"},
			want:        ProfileNone,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			getenv := func(name string) string { return test.environment[name] }
			if got := detectProfile(test.isTerminal, getenv); got != test.want {
				t.Fatalf("detectProfile() = %s, want %s", got, test.want)
			}
		})
	}
}

func TestProfileConvertDegradationLadder(t *testing.T) {
	tests := []struct {
		hex     string
		red     uint8
		green   uint8
		blue    uint8
		ansi256 uint8
		ansi16  uint8
	}{
		{hex: "#000000", ansi256: 16, ansi16: 0},
		{hex: "#ff0000", red: 255, ansi256: 196, ansi16: 9},
		{hex: "#00ff00", green: 255, ansi256: 46, ansi16: 10},
		{hex: "#0000ff", blue: 255, ansi256: 21, ansi16: 12},
		{hex: "#808080", red: 128, green: 128, blue: 128, ansi256: 244, ansi16: 8},
		{hex: "#ffffff", red: 255, green: 255, blue: 255, ansi256: 231, ansi16: 15},
	}

	for _, test := range tests {
		t.Run(test.hex, func(t *testing.T) {
			trueColor, err := ProfileTrueColor.Convert(test.hex)
			if err != nil {
				t.Fatalf("truecolor conversion: %v", err)
			}
			if trueColor != (Color{Profile: ProfileTrueColor, Red: test.red, Green: test.green, Blue: test.blue}) {
				t.Fatalf("truecolor conversion = %#v", trueColor)
			}

			ansi256, err := ProfileANSI256.Convert(test.hex)
			if err != nil {
				t.Fatalf("256-color conversion: %v", err)
			}
			if ansi256.Profile != ProfileANSI256 || ansi256.Index != test.ansi256 {
				t.Fatalf("256-color conversion = %#v, want index %d", ansi256, test.ansi256)
			}

			ansi16, err := ProfileANSI16.Convert(test.hex)
			if err != nil {
				t.Fatalf("16-color conversion: %v", err)
			}
			if ansi16.Profile != ProfileANSI16 || ansi16.Index != test.ansi16 {
				t.Fatalf("16-color conversion = %#v, want index %d", ansi16, test.ansi16)
			}

			none, err := ProfileNone.Convert(test.hex)
			if err != nil {
				t.Fatalf("no-color conversion: %v", err)
			}
			if none != (Color{Profile: ProfileNone}) {
				t.Fatalf("no-color conversion = %#v", none)
			}
		})
	}
}

func TestProfileConvertUsesColorCubeAndGrayscaleRamp(t *testing.T) {
	for _, test := range []struct {
		name string
		hex  string
		want uint8
	}{
		{name: "cube", hex: "#5f87af", want: 67},
		{name: "grayscale", hex: "#767676", want: 243},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := ProfileANSI256.Convert(test.hex)
			if err != nil {
				t.Fatal(err)
			}
			if got.Index != test.want {
				t.Fatalf("Convert(%q) index = %d, want %d", test.hex, got.Index, test.want)
			}
		})
	}
}

func TestSemanticPaletteUsesApprovedColors(t *testing.T) {
	tests := []struct {
		name string
		got  Color
		hex  string
	}{
		{name: "accent", got: Accent(ProfileTrueColor), hex: "#5dcaa5"},
		{name: "warn", got: Warn(ProfileTrueColor), hex: "#efa027"},
		{name: "success", got: Success(ProfileTrueColor), hex: "#97c459"},
		{name: "repo", got: Repo(ProfileTrueColor), hex: "#afa9ec"},
		{name: "repo selected", got: RepoSelected(ProfileTrueColor), hex: "#c9c3f7"},
		{name: "n0", got: N0(ProfileTrueColor), hex: "#2c3145"},
		{name: "n1", got: N1(ProfileTrueColor), hex: "#3d4257"},
		{name: "n2", got: N2(ProfileTrueColor), hex: "#6b7191"},
		{name: "n3", got: N3(ProfileTrueColor), hex: "#8b90ab"},
		{name: "n4", got: N4(ProfileTrueColor), hex: "#c8cad8"},
		{name: "n5", got: N5(ProfileTrueColor), hex: "#e6e8f2"},
		{name: "selection background", got: SelectionBackground(ProfileTrueColor), hex: "#232a42"},
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

	if _, assigned := Info(ProfileTrueColor); assigned {
		t.Fatal("reserved info role is assigned")
	}
}

func TestSemanticHuesRemainDistinctInANSI16(t *testing.T) {
	hues := []Color{
		Accent(ProfileANSI16),
		Warn(ProfileANSI16),
		Success(ProfileANSI16),
		Repo(ProfileANSI16),
	}
	for index, hue := range hues {
		for other := index + 1; other < len(hues); other++ {
			if hue.Index == hues[other].Index {
				t.Fatalf("hues %d and %d both degrade to ANSI color %d", index, other, hue.Index)
			}
		}
	}

	const defaultBackgroundIndex = 0
	if got := SelectionBackground(ProfileANSI16).Index; got == defaultBackgroundIndex {
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
	foreground := Paint(ProfileTrueColor, RoleWarn, "warning")
	got := SelectRow(ProfileTrueColor, foreground+" tail")
	wantForeground := colorSequence(Warn(ProfileTrueColor), false)
	wantBackground := colorSequence(SelectionBackground(ProfileTrueColor), true)
	if !strings.Contains(got, wantForeground) {
		t.Fatalf("selected row lost foreground sequence: %q", got)
	}
	if strings.Count(got, wantBackground) < 2 {
		t.Fatalf("selected row did not restore background after cell reset: %q", got)
	}
	if strings.Contains(got, "\x1b[7m") {
		t.Fatalf("selected row uses reverse video: %q", got)
	}
	if !strings.Contains(got, boldSequence) {
		t.Fatalf("selected row does not brighten foreground: %q", got)
	}
	if plain := SelectRow(ProfileNone, "plain"); plain != "plain" {
		t.Fatalf("no-color selected row = %q", plain)
	}
}

func TestNoRawSGRLiteralsOutsideTheme(t *testing.T) {
	pattern := regexp.MustCompile(`\\x1b\[[0-9;]*m`)
	var paths []string
	err := filepath.WalkDir(".", func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" {
			return nil
		}
		name := entry.Name()
		if strings.HasSuffix(name, "_test.go") || name == "theme.go" || name == "width.go" {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		// #nosec G304 -- WalkDir supplied paths rooted at this package for this source gate.
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if pattern.Match(source) {
			t.Errorf("%s contains a raw ANSI SGR literal", path)
		}
	}
}

func TestProfileConvertRejectsInvalidColors(t *testing.T) {
	for _, value := range []string{"", "ffffff", "#fff", "#gg0000"} {
		if _, err := ProfileTrueColor.Convert(value); err == nil {
			t.Fatalf("Convert(%q) unexpectedly succeeded", value)
		}
	}
	if _, err := Profile(99).Convert("#ffffff"); err == nil {
		t.Fatal("unknown profile conversion unexpectedly succeeded")
	}
}
