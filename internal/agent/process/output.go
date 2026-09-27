package process

import (
	"errors"
	"io"
	"os"
	"sync"
	"time"
)

// outputPipe remains readable after Cmd.Wait reaps the provider. EOF normally
// ends draining; a bounded grace period handles descendants holding pipes open.
type outputPipe struct {
	file *os.File
	done chan struct{}
	once sync.Once
}

func newOutputPipe() (*outputPipe, *os.File, error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	return &outputPipe{file: reader, done: make(chan struct{})}, writer, nil
}

func (p *outputPipe) Read(buf []byte) (int, error) {
	n, err := p.file.Read(buf)
	if errors.Is(err, os.ErrClosed) {
		// Only drainOutput closes this private file, after provider exit.
		err = io.EOF
	}
	if err != nil {
		p.once.Do(func() { close(p.done) })
	}
	return n, err
}

func drainOutput(pipes []*outputPipe) {
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
wait:
	for _, pipe := range pipes {
		select {
		case <-pipe.done:
		case <-timer.C:
			break wait
		}
	}
	for _, pipe := range pipes {
		_ = pipe.file.Close()
	}
}
