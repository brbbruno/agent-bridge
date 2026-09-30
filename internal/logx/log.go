package logx

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const maxLogBytes int64 = 5 << 20

type Logger struct {
	mu   sync.Mutex
	path string
}

func New(path string) *Logger { return &Logger{path: path} }

func (l *Logger) Errorf(format string, args ...any) { l.write("ERRO", format, args...) }
func (l *Logger) Infof(format string, args ...any)  { l.write("INFO", format, args...) }

func (l *Logger) write(level, format string, args ...any) {
	if l == nil || l.path == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = os.MkdirAll(filepath.Dir(l.path), 0o700)
	if info, err := os.Stat(l.path); err == nil && info.Size() >= maxLogBytes {
		old := l.path + ".1"
		_ = os.Remove(old)
		_ = os.Rename(l.path, old)
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(f, "%s [%s] %s\n", time.Now().Format(time.RFC3339), level, fmt.Sprintf(format, args...))
	_ = f.Close()
}
