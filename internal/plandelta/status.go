package plandelta

import (
	"errors"
	"strings"
)

// statusRecord retains exact lookup paths; it is never a display label.
type statusRecord struct {
	Path, OldPath, XY, Fields string
	Kind                      byte
}

// parseStatus consumes complete porcelain-v2 logical records. The Git capture
// layer drops capped partial records; an uncapped malformed record is an error.
func parseStatus(raw string) ([]statusRecord, error) {
	var records []statusRecord
	seen := map[string]bool{}
	for raw != "" {
		line, rest, ok := strings.Cut(raw, "\x00")
		if !ok || len(line) < 3 || line[1] != ' ' {
			return nil, errors.New("malformed porcelain record")
		}
		raw = rest
		r := statusRecord{Kind: line[0], Fields: line}
		switch r.Kind {
		case '?', '!':
			r.Path = line[2:]
		case '1', '2', 'u':
			n := 9
			if r.Kind == '2' {
				n = 10
			}
			if r.Kind == 'u' {
				n = 11
			}
			p := strings.SplitN(line, " ", n)
			if len(p) != n {
				return nil, errors.New("malformed porcelain fields")
			}
			for _, f := range p {
				if f == "" {
					return nil, errors.New("empty porcelain field")
				}
			}
			r.XY, r.Path = p[1], p[n-1]
			if len(r.XY) != 2 || strings.Trim(r.XY, ".MADRCUT") != "" {
				return nil, errors.New("invalid porcelain status")
			}
			validSubmodule := len(p[2]) == 4 && p[2][0] == 'S' && strings.ContainsRune(".C", rune(p[2][1])) && strings.ContainsRune(".M", rune(p[2][2])) && strings.ContainsRune(".U", rune(p[2][3]))
			if p[2] != "N..." && !validSubmodule {
				return nil, errors.New("invalid submodule status")
			}
			modeEnd, hashEnd := 6, 8
			if r.Kind == 'u' {
				modeEnd, hashEnd = 7, 10
			}
			for _, mode := range p[3:modeEnd] {
				if len(mode) != 6 || strings.Trim(mode, "01234567") != "" {
					return nil, errors.New("invalid porcelain mode")
				}
			}
			for _, hash := range p[modeEnd:hashEnd] {
				if strings.Trim(hash, "0123456789abcdef") != "" {
					return nil, errors.New("invalid porcelain object")
				}
			}
			if r.Kind == '2' {
				score := p[8]
				if len(score) < 2 || len(score) > 4 || !strings.ContainsRune("RC", rune(score[0])) || strings.Trim(score[1:], "0123456789") != "" || (len(score) == 4 && score[1:] > "100") {
					return nil, errors.New("invalid rename score")
				}
				r.OldPath, raw, ok = strings.Cut(raw, "\x00")
				if !ok || r.OldPath == "" {
					return nil, errors.New("incomplete rename")
				}
			}
		default:
			return nil, errors.New("unknown porcelain record")
		}
		if excludedDeltaPath(r.Path) || excludedDeltaPath(r.OldPath) {
			continue
		}
		if err := validatePath(r.Path); err != nil {
			return nil, err
		}
		if r.OldPath != "" {
			if err := validatePath(r.OldPath); err != nil {
				return nil, err
			}
		}
		if seen[r.Path] {
			return nil, errors.New("duplicate porcelain path")
		}
		seen[r.Path] = true
		records = append(records, r)
	}
	return records, nil
}
