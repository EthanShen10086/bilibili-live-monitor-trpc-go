//go:build darwin || linux

package monitor

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/EthanShen10086/bilibili-live-monitor-trpc-go/internal/resource"

	"golang.org/x/sys/unix"
)

// CaptureManagedLogs redirects descriptors, including cached framework log streams.
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
				resource.LogError("restore_log_descriptor", unix.Dup2(s.saved, s.fd))
				resource.LogError("restore_log_descriptor", unix.Close(s.saved))
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
			resource.LogError("restore_log_descriptor", unix.Close(saved))
			restore()
			return nil, e
		}
		if e = unix.Dup2(int(w.Fd()), fd); e != nil {
			resource.Close(r)
			resource.Close(w)
			resource.LogError("restore_log_descriptor", unix.Close(saved))
			restore()
			return nil, e
		}
		resource.Close(w)
		done := make(chan struct{})
		streams = append(streams, stream{fd, saved, done})
		go copyLogs(file, r, done)
	}
	return restore, nil
}
