//go:build darwin

package term

import (
	"syscall"
	"time"
	"unsafe"
)

func systemTerminalOperations() terminalOperations {
	return terminalOperations{
		getAttributes: func(fd uintptr) (syscall.Termios, error) {
			var value syscall.Termios
			//nolint:gosec // The termios ioctl API requires a pointer to its output buffer.
			if err := terminalIOCTL(fd, uintptr(syscall.TIOCGETA), unsafe.Pointer(&value)); err != nil {
				return syscall.Termios{}, err
			}
			return value, nil
		},
		setAttributes: func(fd uintptr, value syscall.Termios) error {
			//nolint:gosec // The termios ioctl API requires a pointer to its input buffer.
			return terminalIOCTL(fd, uintptr(syscall.TIOCSETA), unsafe.Pointer(&value))
		},
		getWindowSize: func(fd uintptr) (windowSize, error) {
			var value windowSize
			//nolint:gosec // The window-size ioctl API requires a pointer to its output buffer.
			if err := terminalIOCTL(fd, uintptr(syscall.TIOCGWINSZ), unsafe.Pointer(&value)); err != nil {
				return windowSize{}, err
			}
			return value, nil
		},
	}
}

func waitForTerminalInput(fd uintptr, timeout time.Duration) (bool, error) {
	var descriptorSet syscall.FdSet
	const bitsPerWord = 32
	word := fd / bitsPerWord
	if word >= uintptr(len(descriptorSet.Bits)) {
		return false, syscall.EINVAL
	}
	deadline := time.Now().Add(timeout)
	for {
		descriptorSet = syscall.FdSet{}
		descriptorSet.Bits[word] |= int32(1) << (fd % bitsPerWord)
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false, nil
		}
		timeoutValue := syscall.NsecToTimeval(remaining.Nanoseconds())
		err := syscall.Select(int(fd+1), &descriptorSet, nil, nil, &timeoutValue)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return false, err
		}
		return descriptorSet.Bits[word]&(int32(1)<<(fd%bitsPerWord)) != 0, nil
	}
}

func terminalIOCTL(fd, request uintptr, value unsafe.Pointer) error {
	for {
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, request, uintptr(value))
		if errno == syscall.EINTR {
			continue
		}
		if errno != 0 {
			return errno
		}
		return nil
	}
}
