//go:build !windows

package terminal

import (
	"os"
	"os/signal"
	"syscall"
)

func watchTerminalResize(resize *Resize) func() {
	events := make(chan os.Signal, 1)
	stop := make(chan struct{})
	done := make(chan struct{})

	signal.Notify(events, syscall.SIGWINCH)

	go func() {
		defer close(done)

		for {
			select {
			case <-events:
				resize.notify()
			case <-stop:
				return
			}
		}
	}()

	return func() {
		signal.Stop(events)
		close(stop)
		<-done
	}
}
