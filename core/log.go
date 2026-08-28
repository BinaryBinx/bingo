package core

import (
	"fmt"
	"log"
	"os"
	"time"
)

// LogLevel 日志级别
type LogLevel int

const (
	// LogLevelDebug 调试级别
	LogLevelDebug LogLevel = iota
	// LogLevelInfo 信息级别
	LogLevelInfo
	// LogLevelWarn 警告级别
	LogLevelWarn
	// LogLevelError 错误级别
	LogLevelError
	// LogLevelFatal 致命级别
	LogLevelFatal
)

// LogLevel 是连续的 iota 值，可直接作为数组索引，避免 map 查找开销
var logLevelNames = [...]string{
	LogLevelDebug: "DEBUG",
	LogLevelInfo:  "INFO",
	LogLevelWarn:  "WARN",
	LogLevelError: "ERROR",
	LogLevelFatal: "FATAL",
}

// Logger 日志记录器
type Logger struct {
	level  LogLevel
	prefix string
	logger *log.Logger
}

// NewLogger 创建新的日志记录器
func NewLogger(level LogLevel, prefix string) *Logger {
	return &Logger{
		level:  level,
		prefix: prefix,
		logger: log.New(os.Stdout, "", 0),
	}
}

// formatLog 格式化日志
func (l *Logger) formatLog(level LogLevel, format string, args ...interface{}) string {
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	message := fmt.Sprintf(format, args...)
	return fmt.Sprintf("[%s] [%s] %s: %s", timestamp, logLevelNames[level], l.prefix, message)
}

// Debug 记录调试级别日志
func (l *Logger) Debug(format string, args ...interface{}) {
	if l.level <= LogLevelDebug {
		l.logger.Println(l.formatLog(LogLevelDebug, format, args...))
	}
}

// Info 记录信息级别日志
func (l *Logger) Info(format string, args ...interface{}) {
	if l.level <= LogLevelInfo {
		l.logger.Println(l.formatLog(LogLevelInfo, format, args...))
	}
}

// Warn 记录警告级别日志
func (l *Logger) Warn(format string, args ...interface{}) {
	if l.level <= LogLevelWarn {
		l.logger.Println(l.formatLog(LogLevelWarn, format, args...))
	}
}

// Error 记录错误级别日志
func (l *Logger) Error(format string, args ...interface{}) {
	if l.level <= LogLevelError {
		l.logger.Println(l.formatLog(LogLevelError, format, args...))
	}
}

// Fatal 记录致命级别日志并退出程序
func (l *Logger) Fatal(format string, args ...interface{}) {
	if l.level <= LogLevelFatal {
		l.logger.Println(l.formatLog(LogLevelFatal, format, args...))
	}
	os.Exit(1)
}

// GetLogLevelFromName 根据名称获取日志级别
func GetLogLevelFromName(name string) LogLevel {
	switch name {
	case "debug":
		return LogLevelDebug
	case "info":
		return LogLevelInfo
	case "warn":
		return LogLevelWarn
	case "error":
		return LogLevelError
	case "fatal":
		return LogLevelFatal
	default:
		return LogLevelInfo
	}
}
