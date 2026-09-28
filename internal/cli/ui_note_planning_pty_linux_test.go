//go:build linux

package cli

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

func openPlanningPTY() (master, slave *os.File, err error) {
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if err != nil {
			_ = master.Close()
		}
	}()

	var unlock int32
	//nolint:gosec // The PTY-unlock ioctl requires a pointer to its input value.
	if err = planningPTYIOCTL(master.Fd(), uintptr(syscall.TIOCSPTLCK), unsafe.Pointer(&unlock)); err != nil {
		return nil, nil, fmt.Errorf("unlock PTY: %w", err)
	}
	var number uint32
	//nolint:gosec // The PTY-number ioctl requires a pointer to its output value.
	if err = planningPTYIOCTL(master.Fd(), uintptr(syscall.TIOCGPTN), unsafe.Pointer(&number)); err != nil {
		return nil, nil, fmt.Errorf("read PTY number: %w", err)
	}
	slave, err = os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open PTY slave: %w", err)
	}
	return master, slave, nil
}

func planningPTYAttributes(fd uintptr) (syscall.Termios, error) {
	var value syscall.Termios
	//nolint:gosec // Test-owned PTY ioctl output buffer.
	err := planningPTYIOCTL(fd, uintptr(syscall.TCGETS), unsafe.Pointer(&value))
	return value, err
}
