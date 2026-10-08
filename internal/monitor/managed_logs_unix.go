//go:build darwin || linux

package monitor

import (
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"sync"
)

// Redirect descriptors, so framework loggers that cached stdout/stderr are captured too.
func CaptureManagedLogs(root string) (func(), error) {
	type stream struct {
		fd, saved int
		done      chan struct{}
	}
	streams := []stream{}
	var once sync.Once
	restore := func() {
		once.Do(func() {
			for _, s := range streams {
				unix.Dup2(s.saved, s.fd)
				unix.Close(s.saved)
			}
			for _, s := range streams {
				<-s.done
			}
		})
	}
	for i, name := range []string{"service.log", "error.log"} {
		fd := i + 1
		file := filepath.Join(root, "var", name)
		if e := AppendBoundedLog(file, nil); e != nil {
			restore()
			return nil, e
		}
		saved, e := unix.Dup(fd)
		if e != nil {
			restore()
			return nil, e
		}
		unix.CloseOnExec(saved)
		r, w, e := os.Pipe()
		if e != nil {
			unix.Close(saved)
			restore()
			return nil, e
		}
		if e = unix.Dup2(int(w.Fd()), fd); e != nil {
			r.Close()
			w.Close()
			unix.Close(saved)
			restore()
			return nil, e
		}
		w.Close()
		done := make(chan struct{})
		streams = append(streams, stream{fd, saved, done})
		go copyLogs(file, r, done)
	}
	return restore, nil
}
