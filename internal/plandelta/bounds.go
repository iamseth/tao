package plandelta

// Presentation bounds are fixed, independent of runtime and merge settings.
const (
	MaxFiles          = 400
	MaxListBytes      = 1048576
	MaxFileDiffBytes  = 524288
	MaxFileDiffLines  = 4000
	MaxLineRunes      = 1024
	MaxUntrackedBytes = 262144
	MaxReasonChars    = 240
)
