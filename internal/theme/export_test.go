package theme

// DistinctTestTheme returns a complete, test-only palette with distinct colors.
func DistinctTestTheme() Theme {
	result := Default()
	result.name = "test-distinct"
	for role := range result.specs {
		result.specs[role] = roleSpec{hex: "#123456", ansi16: 1}
	}
	return result
}
