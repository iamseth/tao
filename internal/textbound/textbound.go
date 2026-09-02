// Package textbound bounds strings by bytes while repairing only the cut
// boundary. Callers are responsible for appending their own truncation markers.
package textbound

// Tail returns at most maxBytes from the end of value. If the byte boundary
// cuts through a UTF-8 encoding, Tail removes continuation bytes from the cut
// edge.
func Tail(value string, maxBytes int) (string, bool) {
	if maxBytes <= 0 {
		return "", true
	}
	if len(value) <= maxBytes {
		return value, false
	}

	start := len(value) - maxBytes
	for skipped := 0; skipped < 3 && start < len(value) && value[start]&0xc0 == 0x80; skipped++ {
		start++
	}
	return value[start:], true
}

// Head returns at most maxBytes from the beginning of value. If the byte
// boundary cuts through a UTF-8 encoding, Head removes the partial encoding
// from the cut edge.
func Head(value string, maxBytes int) (string, bool) {
	if maxBytes <= 0 {
		return "", true
	}
	if len(value) <= maxBytes {
		return value, false
	}

	end := maxBytes
	if value[end]&0xc0 == 0x80 {
		end--
		for walked := 0; walked < 3 && end > 0 && value[end]&0xc0 == 0x80; walked++ {
			end--
		}
	}
	return value[:end], true
}
