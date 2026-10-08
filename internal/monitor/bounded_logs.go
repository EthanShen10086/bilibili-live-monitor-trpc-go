package monitor

import (
	"io"
	"os"
	"path/filepath"
	"sync"
)

const LogLimit = 2 * 1024 * 1024
const logChunkLimit = 64 * 1024

func trimLog(file string) error {
	stat, e := os.Stat(file)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	if stat.Size() <= LogLimit {
		return nil
	}
	f, e := os.Open(file)
	if e != nil {
		return e
	}
	tail := make([]byte, LogLimit)
	_, e = f.ReadAt(tail, stat.Size()-LogLimit)
	f.Close()
	if e != nil {
		return e
	}
	return os.WriteFile(file, tail, 0600)
}
func AppendBoundedLog(file string, p []byte) error {
	if e := os.MkdirAll(filepath.Dir(file), 0700); e != nil {
		return e
	}
	if len(p) > logChunkLimit {
		p = p[len(p)-logChunkLimit:]
	}
	for _, suffix := range []string{"", ".1", ".2", ".3"} {
		if e := trimLog(file + suffix); e != nil {
			return e
		}
	}
	if stat, e := os.Stat(file); e == nil && stat.Size()+int64(len(p)) > LogLimit {
		for _, pair := range [][2]string{{".2", ".3"}, {".1", ".2"}, {"", ".1"}} {
			if e = os.Rename(file+pair[0], file+pair[1]); e != nil && !os.IsNotExist(e) {
				return e
			}
		}
	}
	f, e := os.OpenFile(file, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	_, e = f.Write(p)
	return e
}

type logWriter struct {
	file string
	mu   sync.Mutex
}

func (w *logWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	e := AppendBoundedLog(w.file, p)
	if e != nil {
		return 0, e
	}
	return len(p), nil
}
func copyLogs(file string, r *os.File, done chan<- struct{}) {
	defer r.Close()
	defer close(done)
	io.CopyBuffer(&logWriter{file: file}, r, make([]byte, 32*1024))
}
