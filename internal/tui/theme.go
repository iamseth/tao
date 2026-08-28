package tui

import (
	"errors"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
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

// Role identifies a semantic palette slot. RoleInfo is deliberately reserved
// without an assigned color until the Plans tab needs it.
type Role uint8

const (
	RoleAccent Role = iota
	RoleWarn
	RoleSuccess
	RoleRepo
	RoleRepoSelected
	RoleInfo
	RoleNeutral0
	RoleNeutral1
	RoleNeutral2
	RoleNeutral3
	RoleNeutral4
	RoleNeutral5
	RoleSelectionBackground
)

type roleSpec struct {
	hex    string
	ansi16 uint8
}

var repoColorRoles = [...]Role{RoleAccent, RoleWarn, RoleSuccess, RoleRepo}

// RoleColor resolves a semantic role for the requested terminal profile. The
// boolean is false for unassigned or unknown roles.
func RoleColor(profile Profile, role Role) (Color, bool) {
	spec, ok := roleSpecification(role)
	if !ok {
		return Color{}, false
	}
	color, err := profile.Convert(spec.hex)
	if err != nil {
		panic(err) // Palette literals are compile-time-owned values.
	}
	if profile == ProfileANSI16 {
		color.Index = spec.ansi16
	}
	return color, true
}

func Accent(profile Profile) Color       { return mustRoleColor(profile, RoleAccent) }
func Warn(profile Profile) Color         { return mustRoleColor(profile, RoleWarn) }
func Success(profile Profile) Color      { return mustRoleColor(profile, RoleSuccess) }
func Repo(profile Profile) Color         { return mustRoleColor(profile, RoleRepo) }
func RepoSelected(profile Profile) Color { return mustRoleColor(profile, RoleRepoSelected) }
func N0(profile Profile) Color           { return mustRoleColor(profile, RoleNeutral0) }
func N1(profile Profile) Color           { return mustRoleColor(profile, RoleNeutral1) }
func N2(profile Profile) Color           { return mustRoleColor(profile, RoleNeutral2) }
func N3(profile Profile) Color           { return mustRoleColor(profile, RoleNeutral3) }
func N4(profile Profile) Color           { return mustRoleColor(profile, RoleNeutral4) }
func N5(profile Profile) Color           { return mustRoleColor(profile, RoleNeutral5) }
func SelectionBackground(profile Profile) Color {
	return mustRoleColor(profile, RoleSelectionBackground)
}

// Info reports the reserved info role as unassigned.
func Info(Profile) (Color, bool) { return Color{}, false }

// RepoColor maps a repository ID to one of the fixed hue slots with FNV-1a.
// Returning the role keeps the assignment independent of terminal profile.
func RepoColor(id string) Role {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(id))
	return repoColorRoles[hash.Sum32()%uint32(len(repoColorRoles))]
}

// Paint applies a semantic foreground role to text.
func Paint(profile Profile, role Role, text string) string {
	color, ok := RoleColor(profile, role)
	if !ok || profile == ProfileNone || text == "" {
		return text
	}
	return colorSequence(color, false) + text + resetSequence
}

// SelectRow fills a row with the selection background while preserving any
// foreground sequences already present. Bold brightens foreground glyphs
// without reverse-video inversion; resets within the row restore both effects.
func SelectRow(profile Profile, row string) string {
	if profile == ProfileNone || row == "" {
		return row
	}
	prefix := boldSequence + colorSequence(SelectionBackground(profile), true)
	row = strings.ReplaceAll(row, resetSequence, resetSequence+prefix)
	return prefix + row + resetSequence
}

func roleSpecification(role Role) (roleSpec, bool) {
	switch role {
	case RoleAccent:
		return roleSpec{hex: "#5dcaa5", ansi16: 14}, true
	case RoleWarn:
		return roleSpec{hex: "#efa027", ansi16: 11}, true
	case RoleSuccess:
		return roleSpec{hex: "#97c459", ansi16: 10}, true
	case RoleRepo:
		return roleSpec{hex: "#afa9ec", ansi16: 13}, true
	case RoleRepoSelected:
		return roleSpec{hex: "#c9c3f7", ansi16: 15}, true
	case RoleNeutral0:
		return roleSpec{hex: "#2c3145", ansi16: 4}, true
	case RoleNeutral1:
		return roleSpec{hex: "#3d4257", ansi16: 6}, true
	case RoleNeutral2:
		return roleSpec{hex: "#6b7191", ansi16: 8}, true
	case RoleNeutral3:
		return roleSpec{hex: "#8b90ab", ansi16: 8}, true
	case RoleNeutral4:
		return roleSpec{hex: "#c8cad8", ansi16: 7}, true
	case RoleNeutral5:
		return roleSpec{hex: "#e6e8f2", ansi16: 15}, true
	case RoleSelectionBackground:
		return roleSpec{hex: "#232a42", ansi16: 4}, true
	default:
		return roleSpec{}, false
	}
}

func mustRoleColor(profile Profile, role Role) Color {
	color, ok := RoleColor(profile, role)
	if !ok {
		panic(fmt.Sprintf("unassigned color role %d", role))
	}
	return color
}

const (
	resetSequence = "\x1b[0m"
	boldSequence  = "\x1b[1m"
)

func colorSequence(color Color, background bool) string {
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

func (p Profile) supportsColor() bool {
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

func detectProfile(isTerminal bool, getenv func(string) string) Profile {
	term := strings.ToLower(strings.TrimSpace(getenv("TERM")))
	if getenv("NO_COLOR") != "" || term == "dumb" {
		return ProfileNone
	}
	if value := strings.TrimSpace(getenv("CLICOLOR_FORCE")); value != "" && value != "0" {
		return profileFromEnvironment(term, getenv("COLORTERM"))
	}
	if !isTerminal || strings.TrimSpace(getenv("CLICOLOR")) == "0" {
		return ProfileNone
	}
	return profileFromEnvironment(term, getenv("COLORTERM"))
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
