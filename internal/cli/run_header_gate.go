package cli

const (
	minRunHeaderRows    = 12
	minRunHeaderColumns = 60
)

func runHeaderEnabled(noRunHeader, stdoutIsTerminal bool, term string, rows, columns int) bool {
	return !noRunHeader && stdoutIsTerminal && term != "dumb" && runHeaderSizeEligible(rows, columns)
}

func runHeaderSizeEligible(rows, columns int) bool {
	return rows >= minRunHeaderRows && columns >= minRunHeaderColumns
}
