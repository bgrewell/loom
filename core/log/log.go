// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package log

import (
	"fmt"
	"io"
	"sync/atomic"
	"time"
)

// Level is a log severity.
type Level int

// Severity levels, increasing.
const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

// String renders the level.
func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "DBG"
	case LevelInfo:
		return "INF"
	case LevelWarn:
		return "WRN"
	default:
		return "ERR"
	}
}

// Logger is an async leveled logger for control/diagnostic output. Log calls
// format a line and hand it to a background writer via a buffered channel; if
// the channel is full the line is dropped and counted, so logging never blocks
// the caller. It is NOT for the data-plane hot path — that uses Ring.
type Logger struct {
	out     io.Writer
	level   Level
	ch      chan string
	done    chan struct{}
	dropped atomic.Uint64
}

// New starts a Logger writing lines >= level to out.
func New(out io.Writer, level Level) *Logger {
	l := &Logger{out: out, level: level, ch: make(chan string, 1024), done: make(chan struct{})}
	go l.run()
	return l
}

func (l *Logger) run() {
	for line := range l.ch {
		_, _ = io.WriteString(l.out, line)
	}
	close(l.done)
}

func (l *Logger) log(lvl Level, msg string) {
	if lvl < l.level {
		return
	}
	line := fmt.Sprintf("%s %s %s\n", time.Now().Format("15:04:05.000"), lvl, msg)
	select {
	case l.ch <- line:
	default:
		l.dropped.Add(1)
	}
}

// Debug logs at debug level.
func (l *Logger) Debug(msg string) { l.log(LevelDebug, msg) }

// Info logs at info level.
func (l *Logger) Info(msg string) { l.log(LevelInfo, msg) }

// Warn logs at warn level.
func (l *Logger) Warn(msg string) { l.log(LevelWarn, msg) }

// Error logs at error level.
func (l *Logger) Error(msg string) { l.log(LevelError, msg) }

// Dropped returns the number of lines dropped because the buffer was full.
func (l *Logger) Dropped() uint64 { return l.dropped.Load() }

// Close flushes buffered lines and stops the writer. The Logger must not be used
// after Close.
func (l *Logger) Close() {
	close(l.ch)
	<-l.done
}
