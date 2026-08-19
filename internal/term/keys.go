package term

import (
	"errors"
	"io"
	"time"
	"unicode"
	"unicode/utf8"
)

// Key identifies a decoded keyboard input.
type Key uint8

const (
	KeyUnknown Key = iota
	KeyRune
	KeyEnter
	KeyTab
	KeyEsc
	KeyBackspace
	KeyArrowUp
	KeyArrowDown
	KeyArrowRight
	KeyArrowLeft
	KeyCtrlC
)

const escapeSequenceTimeout = 50 * time.Millisecond

// KeyEvent is one decoded keyboard input. Rune is set only when Key is
// KeyRune.
type KeyEvent struct {
	Key  Key
	Rune rune
}

// Decoder reads and decodes terminal keyboard input. It buffers each read so
// one read may yield multiple events.
type Decoder struct {
	reader  io.Reader
	buffer  []byte
	readErr error
}

// NewDecoder creates a key decoder over reader.
func NewDecoder(reader io.Reader) *Decoder {
	return &Decoder{reader: reader}
}

// ReadKey returns the next keyboard event.
func (d *Decoder) ReadKey() (KeyEvent, error) {
	if err := d.ensureInput(); err != nil {
		return KeyEvent{}, err
	}

	switch d.buffer[0] {
	case 0x03:
		d.consume(1)
		return KeyEvent{Key: KeyCtrlC}, nil
	case '\r', '\n':
		d.consume(1)
		return KeyEvent{Key: KeyEnter}, nil
	case '\t':
		d.consume(1)
		return KeyEvent{Key: KeyTab}, nil
	case 0x08, 0x7f:
		d.consume(1)
		return KeyEvent{Key: KeyBackspace}, nil
	case 0x1b:
		return d.readEscape()
	}

	if err := d.ensureRune(); err != nil {
		return KeyEvent{}, err
	}
	r, size := utf8.DecodeRune(d.buffer)
	d.consume(size)
	if r == utf8.RuneError && size == 1 {
		return KeyEvent{Key: KeyUnknown}, nil
	}
	if unicode.IsPrint(r) {
		return KeyEvent{Key: KeyRune, Rune: r}, nil
	}
	return KeyEvent{Key: KeyUnknown}, nil
}

func (d *Decoder) readEscape() (KeyEvent, error) {
	deadline := time.Now().Add(escapeSequenceTimeout)
	complete, err := d.ensureEscapeBytes(2, deadline)
	if err != nil {
		return KeyEvent{}, err
	}
	if !complete || d.buffer[1] != '[' {
		return d.consumeEscape(), nil
	}

	complete, err = d.ensureEscapeBytes(3, deadline)
	if err != nil {
		return KeyEvent{}, err
	}
	if !complete {
		return d.consumeEscape(), nil
	}

	var key Key
	switch d.buffer[2] {
	case 'A':
		key = KeyArrowUp
	case 'B':
		key = KeyArrowDown
	case 'C':
		key = KeyArrowRight
	case 'D':
		key = KeyArrowLeft
	}
	if key != KeyUnknown {
		d.consume(3)
		return KeyEvent{Key: key}, nil
	}
	return d.consumeEscape(), nil
}

func (d *Decoder) ensureEscapeBytes(count int, deadline time.Time) (bool, error) {
	for len(d.buffer) < count {
		if d.readErr != nil {
			return false, nil //nolint:nilerr // buffered input is decoded before its trailing read error
		}
		if file, ok := d.reader.(interface{ Fd() uintptr }); ok {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return false, nil
			}
			ready, err := waitForTerminalInput(file.Fd(), remaining)
			if err != nil {
				return false, err
			}
			if !ready {
				return false, nil
			}
		}
		if err := d.readMore(); err != nil {
			if errors.Is(err, io.EOF) {
				return false, nil
			}
			return false, err
		}
	}
	return true, nil
}

func (d *Decoder) consumeEscape() KeyEvent {
	d.consume(1)
	return KeyEvent{Key: KeyEsc}
}

func (d *Decoder) ensureInput() error {
	if len(d.buffer) > 0 {
		return nil
	}
	if d.readErr != nil {
		return d.readErr
	}
	return d.readMore()
}

func (d *Decoder) ensureRune() error {
	for !utf8.FullRune(d.buffer) {
		if d.readErr != nil {
			if errors.Is(d.readErr, io.EOF) {
				return io.ErrUnexpectedEOF
			}
			return d.readErr
		}
		if err := d.readMore(); err != nil {
			return err
		}
	}
	return nil
}

func (d *Decoder) readMore() error {
	var next [64]byte
	n, err := d.reader.Read(next[:])
	if n > 0 {
		d.buffer = append(d.buffer, next[:n]...)
		d.readErr = err
		return nil
	}
	if err != nil {
		d.readErr = err
		return err
	}
	return io.ErrNoProgress
}

func (d *Decoder) consume(count int) {
	d.buffer = d.buffer[count:]
}
