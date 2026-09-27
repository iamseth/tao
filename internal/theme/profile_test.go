package theme

import "testing"

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
			if got := DetectProfile(test.isTerminal, getenv); got != test.want {
				t.Fatalf("DetectProfile() = %s, want %s", got, test.want)
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
