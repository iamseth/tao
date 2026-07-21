package runqueue

import (
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// WatchStopSignals requests a queue stop when the process receives SIGINT or
// SIGTERM. The returned stop function unregisters the signal handler and waits
// for the watcher goroutine to exit.
func WatchStopSignals(m *Manager) (stop func()) {
	signals := make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	stopped := watchStopChannel(m, signals, done)

	var once sync.Once
	return func() {
		once.Do(func() {
			signal.Stop(signals)
			close(done)
			<-stopped
		})
	}
}

func watchStopChannel(m *Manager, ch <-chan os.Signal, done <-chan struct{}) <-chan struct{} {
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case _, ok := <-ch:
			if ok && m != nil {
				m.RequestStop()
			}
		case <-done:
		}
	}()
	return stopped
}
