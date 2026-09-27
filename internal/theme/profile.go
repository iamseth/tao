package theme

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/iamseth/tao/internal/term"
)

// Profile describes the color resolution available to the renderer.
type Profile uint8

const (
	ProfileNone Profile = iota
	ProfileANSI16
	ProfileANSI256
	ProfileTrueColor
)

// Color is the profile-specific representation of an authored RGB color.
// Index is populated for ANSI profiles; RGB is retained for truecolor.
type Color struct {
	Profile Profile
	Red     uint8
	Green   uint8
	Blue    uint8
	Index   uint8
}

func (p Profile) String() string {
	switch p {
	case ProfileTrueColor:
		return "truecolor"
	case ProfileANSI256:
		return "256-color"
	case ProfileANSI16:
		return "16-color"
	default:
		return "none"
	}
}

func (p Profile) Enabled() bool {
	return p != ProfileNone
}

// Convert degrades an authored #RRGGBB color to the profile's best available
// representation without consulting terminal or process state.
func (p Profile) Convert(hex string) (Color, error) {
	red, green, blue, err := parseHexColor(hex)
	if err != nil {
		return Color{}, err
	}
	color := Color{Profile: p, Red: red, Green: green, Blue: blue}
	switch p {
	case ProfileNone:
		color.Red, color.Green, color.Blue = 0, 0, 0
	case ProfileANSI16:
		color.Index = ansi256ToANSI16(rgbToANSI256(red, green, blue))
	case ProfileANSI256:
		color.Index = rgbToANSI256(red, green, blue)
	case ProfileTrueColor:
	default:
		return Color{}, fmt.Errorf("unknown color profile %d", p)
	}
	return color, nil
}

func DetectProfile(isTerminal bool, getenv func(string) string) Profile {
	if !term.ColorEnabled(isTerminal, getenv) {
		return ProfileNone
	}
	return profileFromEnvironment(getenv("TERM"), getenv("COLORTERM"))
}

func profileFromEnvironment(term, colorTerm string) Profile {
	colorTerm = strings.ToLower(strings.TrimSpace(colorTerm))
	if strings.Contains(colorTerm, "truecolor") || strings.Contains(colorTerm, "24bit") {
		return ProfileTrueColor
	}
	if strings.Contains(strings.ToLower(term), "256color") {
		return ProfileANSI256
	}
	return ProfileANSI16
}

func parseHexColor(value string) (uint8, uint8, uint8, error) {
	if len(value) != 7 || value[0] != '#' {
		return 0, 0, 0, errors.New("color must use #RRGGBB format")
	}
	parsed, err := strconv.ParseUint(value[1:], 16, 24)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("parse color %q: %w", value, err)
	}
	return uint8((parsed >> 16) & 0xff), uint8((parsed >> 8) & 0xff), uint8(parsed & 0xff), nil
}

func rgbToANSI256(red, green, blue uint8) uint8 {
	bestIndex := uint8(16)
	bestDistance := int(^uint(0) >> 1)
	for index := 16; index <= 255; index++ {
		candidateRed, candidateGreen, candidateBlue := ansi256RGB(uint8(index))
		distance := colorDistance(red, green, blue, candidateRed, candidateGreen, candidateBlue)
		if distance < bestDistance {
			bestDistance = distance
			bestIndex = uint8(index)
		}
	}
	return bestIndex
}

func ansi256ToANSI16(index uint8) uint8 {
	red, green, blue := ansi256RGB(index)
	bestIndex := uint8(0)
	bestDistance := int(^uint(0) >> 1)
	for candidate := uint8(0); candidate < 16; candidate++ {
		candidateRed, candidateGreen, candidateBlue := ansi16RGB(candidate)
		distance := colorDistance(red, green, blue, candidateRed, candidateGreen, candidateBlue)
		if distance < bestDistance {
			bestDistance = distance
			bestIndex = candidate
		}
	}
	return bestIndex
}

func ansi256RGB(index uint8) (uint8, uint8, uint8) {
	if index < 16 {
		return ansi16RGB(index)
	}
	if index >= 232 {
		value := uint8(8) + uint8(10)*(index-232)
		return value, value, value
	}
	cube := int(index) - 16
	return cubeLevel(cube / 36), cubeLevel((cube / 6) % 6), cubeLevel(cube % 6)
}

func cubeLevel(value int) uint8 {
	levels := [...]uint8{0, 95, 135, 175, 215, 255}
	return levels[value]
}

func ansi16RGB(index uint8) (uint8, uint8, uint8) {
	palette := [...]struct{ red, green, blue uint8 }{
		{0, 0, 0}, {128, 0, 0}, {0, 128, 0}, {128, 128, 0},
		{0, 0, 128}, {128, 0, 128}, {0, 128, 128}, {192, 192, 192},
		{128, 128, 128}, {255, 0, 0}, {0, 255, 0}, {255, 255, 0},
		{0, 0, 255}, {255, 0, 255}, {0, 255, 255}, {255, 255, 255},
	}
	color := palette[index%16]
	return color.red, color.green, color.blue
}

func colorDistance(red, green, blue, otherRed, otherGreen, otherBlue uint8) int {
	redDelta := int(red) - int(otherRed)
	greenDelta := int(green) - int(otherGreen)
	blueDelta := int(blue) - int(otherBlue)
	return redDelta*redDelta + greenDelta*greenDelta + blueDelta*blueDelta
}
