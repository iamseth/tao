package gitops

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// NameStatusEntry preserves exact Git paths, including rename/copy sources.
type NameStatusEntry struct{ Status, OldPath, Path string }

// NumstatEntry carries line counts; binary entries have zero counts.
type NumstatEntry struct {
	Added, Deleted int
	Binary         bool
	Path, OldPath  string
}

func validDeltaStatus(s string) bool {
	if len(s) == 1 {
		return strings.ContainsRune("AMDTUXB", rune(s[0]))
	}
	if len(s) < 2 || !strings.ContainsRune("RCM", rune(s[0])) {
		return false
	}
	n, err := decimalCount(s[1:])
	return err == nil && n <= 100
}

func decimalCount(s string) (int, error) {
	if s == "" {
		return 0, errors.New("empty count")
	}
	for _, b := range []byte(s) {
		if b < '0' || b > '9' {
			return 0, errors.New("invalid count")
		}
	}
	return strconv.Atoi(s)
}

func numstatHeader(s string) (NumstatEntry, error) {
	fields := strings.SplitN(s, "\t", 3)
	if len(fields) != 3 {
		return NumstatEntry{}, errors.New("malformed numstat")
	}
	e := NumstatEntry{Path: fields[2]}
	if fields[0] == "-" && fields[1] == "-" {
		e.Binary = true
		return e, nil
	}
	var err error
	e.Added, err = decimalCount(fields[0])
	if err != nil {
		return e, err
	}
	e.Deleted, err = decimalCount(fields[1])
	return e, err
}

func validSubmoduleStatus(s string) bool {
	return s == "N..." || (len(s) == 4 && s[0] == 'S' && strings.ContainsRune(".C", rune(s[1])) && strings.ContainsRune(".M", rune(s[2])) && strings.ContainsRune(".U", rune(s[3])))
}

// listExtra validates a complete first field and determines logical framing.
func listExtra(first, kind string) (int, error) {
	switch kind {
	case "name":
		if !validDeltaStatus(first) {
			return 0, errors.New("invalid name-status status")
		}
		if first[0] == 'R' || first[0] == 'C' {
			return 2, nil
		}
		return 1, nil
	case "num":
		e, err := numstatHeader(first)
		if err != nil {
			return 0, err
		}
		if e.Path == "" {
			return 2, nil
		}
		return 0, nil
	case "status":
		if len(first) < 3 || first[1] != ' ' {
			return 0, errors.New("malformed porcelain record")
		}
		fields := 0
		extra := 0
		switch first[0] {
		case '?', '!':
			return 0, nil
		case '1':
			fields = 9
		case '2':
			fields = 10
			extra = 1
		case 'u':
			fields = 11
		default:
			return 0, errors.New("invalid porcelain type")
		}
		parts := strings.SplitN(first, " ", fields)
		if len(parts) != fields {
			return 0, errors.New("malformed porcelain fields")
		}
		for _, part := range parts {
			if part == "" {
				return 0, errors.New("empty porcelain field")
			}
		}
		if len(parts[1]) != 2 || strings.Trim(parts[1], ".MADRCUT?!") != "" || !validSubmoduleStatus(parts[2]) {
			return 0, errors.New("malformed porcelain status")
		}
		modeEnd, hashEnd := 6, 8
		if first[0] == 'u' {
			modeEnd, hashEnd = 7, 10
		}
		for _, mode := range parts[3:modeEnd] {
			if len(mode) != 6 || strings.Trim(mode, "01234567") != "" {
				return 0, errors.New("invalid porcelain mode")
			}
		}
		for _, hash := range parts[modeEnd:hashEnd] {
			if strings.Trim(hash, "0123456789abcdef") != "" {
				return 0, errors.New("invalid porcelain object")
			}
		}
		if extra == 1 && (!validDeltaStatus(parts[8]) || !strings.ContainsRune("RC", rune(parts[8][0]))) {
			return 0, errors.New("invalid porcelain rename score")
		}
		return extra, nil
	}
	return 0, errors.New("unknown list format")
}

// completeList walks tokens without allocating a split of the entire capture.
// Inner NULs are not record boundaries for rename/copy records.
func completeList(raw string, truncated bool, kind string) (string, error) {
	end := 0
	for pos := 0; pos < len(raw); {
		first, rest, ok := strings.Cut(raw[pos:], "\x00")
		if !ok {
			if truncated {
				return raw[:end], nil
			}
			return "", errors.New("unterminated list record")
		}
		extra, err := listExtra(first, kind)
		if err != nil {
			return "", err
		}
		pos = len(raw) - len(rest)
		for range extra {
			path, rest, ok := strings.Cut(raw[pos:], "\x00")
			if !ok {
				if truncated {
					return raw[:end], nil
				}
				return "", errors.New("incomplete path record")
			}
			if path == "" {
				return "", errors.New("empty list path")
			}
			pos = len(raw) - len(rest)
		}
		end = pos
	}
	return raw[:end], nil
}

func (c Client) deltaList(ctx context.Context, maxBytes int, kind string, args []string) (string, bool, error) {
	if maxBytes <= 0 {
		return "", false, errors.New("list limit must be positive")
	}
	out := boundedWriter{limit: maxBytes}
	stderr := boundedWriter{limit: probeOutputLimit}
	err := c.readOnlyClient().git(ctx, args, &out, &stderr)
	if ctx.Err() != nil {
		return "", out.truncated, ctx.Err()
	}
	if err != nil {
		return "", out.truncated, commandError(args, err, probeStderr(&stderr))
	}
	raw, err := completeList(out.String(), out.truncated, kind)
	if err != nil {
		return "", out.truncated, fmt.Errorf("git %s list: %w", kind, err)
	}
	return raw, out.truncated, nil
}

func listDiffArgs(format string, args []string) []string {
	return append([]string{"diff", "--no-color", "--no-ext-diff", "--no-textconv", "--no-renames", format, "-z"}, args...)
}

// DiffNameStatusZ captures complete name/status records with bounded output.
func (c Client) DiffNameStatusZ(ctx context.Context, maxBytes int, args ...string) ([]NameStatusEntry, bool, error) {
	raw, truncated, err := c.deltaList(ctx, maxBytes, "name", listDiffArgs("--name-status", args))
	if err != nil {
		return nil, truncated, err
	}
	entries, err := ParseNameStatusZ(raw)
	return entries, truncated, err
}

// DiffNumstatZ captures complete numstat records with bounded output.
func (c Client) DiffNumstatZ(ctx context.Context, maxBytes int, args ...string) ([]NumstatEntry, bool, error) {
	raw, truncated, err := c.deltaList(ctx, maxBytes, "num", listDiffArgs("--numstat", args))
	if err != nil {
		return nil, truncated, err
	}
	var entries []NumstatEntry
	for raw != "" {
		var first string
		first, raw, _ = strings.Cut(raw, "\x00")
		e, err := numstatHeader(first)
		if err != nil {
			return nil, truncated, err
		}
		if e.Path == "" {
			e.OldPath, raw, _ = strings.Cut(raw, "\x00")
			e.Path, raw, _ = strings.Cut(raw, "\x00")
		}
		entries = append(entries, e)
	}
	return entries, truncated, nil
}

// StatusPorcelainV2Z retains only complete logical porcelain-v2 records.
func (c Client) StatusPorcelainV2Z(ctx context.Context, maxBytes int) (string, bool, error) {
	return c.deltaList(ctx, maxBytes, "status", []string{"status", "--porcelain=v2", "-z", "--untracked-files=all", "--no-renames"})
}
