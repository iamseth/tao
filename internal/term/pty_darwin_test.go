//go:build darwin

package term

import (
	"bytes"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

func openPTY() (master, slave *os.File, err error) {
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if err != nil {
			_ = master.Close()
		}
	}()

	if err = terminalIOCTL(master.Fd(), uintptr(syscall.TIOCPTYGRANT), nil); err != nil {
		return nil, nil, fmt.Errorf("grant PTY: %w", err)
	}
	if err = terminalIOCTL(master.Fd(), uintptr(syscall.TIOCPTYUNLK), nil); err != nil {
		return nil, nil, fmt.Errorf("unlock PTY: %w", err)
	}
	var name [128]byte
	//nolint:gosec // The PTY-name ioctl requires a pointer to its output buffer.
	if err = terminalIOCTL(master.Fd(), uintptr(syscall.TIOCPTYGNAME), unsafe.Pointer(&name[0])); err != nil {
		return nil, nil, fmt.Errorf("read PTY name: %w", err)
	}
	end := bytes.IndexByte(name[:], 0)
	if end < 0 {
		return nil, nil, fmt.Errorf("PTY name is not null terminated")
	}
	slave, err = os.OpenFile(string(name[:end]), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open PTY slave: %w", err)
	}
	return master, slave, nil
}
