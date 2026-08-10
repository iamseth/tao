package term

import (
	"io"
	"syscall"
	"testing"
	"time"
)

func TestTerminalEnterRawKeepsDashboardRowsAtColumnZeroOnPTY(t *testing.T) {
	master, slave, err := openPTY()
	if err != nil {
		t.Fatalf("open PTY: %v", err)
	}
	t.Cleanup(func() {
		_ = slave.Close()
		_ = master.Close()
	})

	operations := systemTerminalOperations()
	attributes, err := operations.getAttributes(slave.Fd())
	if err != nil {
		t.Fatalf("get PTY attributes: %v", err)
	}
	attributes.Oflag |= syscall.OPOST | syscall.ONLCR
	if err := operations.setAttributes(slave.Fd(), attributes); err != nil {
		t.Fatalf("enable PTY newline processing: %v", err)
	}

	terminal := NewTerminal(slave)
	if err := terminal.EnterRaw(); err != nil {
		t.Fatalf("EnterRaw() error = %v", err)
	}
	t.Cleanup(func() { _ = terminal.Restore() })

	const frame = "first row\nsecond row\n"
	if _, err := io.WriteString(slave, frame); err != nil {
		t.Fatalf("write dashboard frame: %v", err)
	}

	const want = "first row\r\nsecond row\r\n"
	if err := master.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set PTY read deadline: %v", err)
	}
	got := make([]byte, len(want))
	if _, err := io.ReadFull(master, got); err != nil {
		t.Fatalf("read dashboard frame: %v", err)
	}
	if string(got) != want {
		t.Fatalf("dashboard frame = %q, want newline processing %q", got, want)
	}
}
