package logging

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
)

type Level int

const (
	DebugLevel Level = iota
	InfoLevel
	WarnLevel
	ErrorLevel
)

type Logger struct {
	mu    sync.Mutex
	out   *log.Logger
	level Level
	file  *os.File
}

func New(level, path string) (*Logger, error) {
	lv := parseLevel(level)
	writers := []io.Writer{os.Stderr}
	var f *os.File
	if path != "" {
		if err := os.MkdirAll(filepathDir(path), 0o700); err != nil {
			return nil, err
		}
		var err error
		f, err = os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, err
		}
		writers = append(writers, f)
	}
	return &Logger{
		out:   log.New(io.MultiWriter(writers...), "", log.LstdFlags|log.LUTC),
		level: lv,
		file:  f,
	}, nil
}

func (l *Logger) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	return l.file.Close()
}

func (l *Logger) Println(v ...interface{}) {
	l.logf(InfoLevel, "%s", fmt.Sprint(v...))
}

func (l *Logger) Debugf(format string, args ...interface{}) {
	l.logf(DebugLevel, format, args...)
}

func (l *Logger) Infof(format string, args ...interface{}) {
	l.logf(InfoLevel, format, args...)
}

func (l *Logger) Warnf(format string, args ...interface{}) {
	l.logf(WarnLevel, format, args...)
}

func (l *Logger) Errorf(format string, args ...interface{}) {
	l.logf(ErrorLevel, format, args...)
}

func (l *Logger) logf(lv Level, format string, args ...interface{}) {
	if l == nil || l.out == nil || lv < l.level {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.out.Printf(format, args...)
}

func parseLevel(s string) Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return DebugLevel
	case "warn":
		return WarnLevel
	case "error":
		return ErrorLevel
	default:
		return InfoLevel
	}
}

func filepathDir(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[:i]
		}
	}
	return "."
}
