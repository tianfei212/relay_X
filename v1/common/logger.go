package common

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// Version: 1.0
// Developer: GPT-4/JOJO
// Date: 2026-02-15

// Logger 全局日志对象
var Logger *slog.Logger
var levelVar slog.LevelVar

// InitLogger 初始化中文日志
// 功能说明: 配置日志格式为 Text，并设置输出到 Stdout
func InitLogger() {
	levelVar.Set(slog.LevelInfo)
	SetOutput(os.Stdout)
}

// SetOutput 设置日志输出目标
func SetOutput(w io.Writer) {
	opts := &slog.HandlerOptions{
		Level: &levelVar,
	}
	// 使用 TextHandler 方便阅读
	handler := slog.NewTextHandler(w, opts)
	Logger = slog.New(handler)
	slog.SetDefault(Logger)
}

func SetLevel(level slog.Level) {
	levelVar.Set(level)
}

func SetLogLevel(level string) error {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "", "info":
		levelVar.Set(slog.LevelInfo)
		return nil
	case "debug":
		levelVar.Set(slog.LevelDebug)
		return nil
	case "warn", "warning":
		levelVar.Set(slog.LevelWarn)
		return nil
	case "error":
		levelVar.Set(slog.LevelError)
		return nil
	default:
		return &slogError{msg: "未知日志等级", level: level}
	}
}

type slogError struct {
	msg   string
	level string
}

func (e *slogError) Error() string {
	return e.msg + ": " + e.level
}

// LogInfo 记录普通信息
func LogInfo(msg string, args ...any) {
	if Logger == nil {
		InitLogger()
	}
	Logger.Info(msg, args...)
}

// LogError 记录错误信息
func LogError(msg string, args ...any) {
	if Logger == nil {
		InitLogger()
	}
	Logger.Error(msg, args...)
}

// LogWarn 记录警告信息
func LogWarn(msg string, args ...any) {
	if Logger == nil {
		InitLogger()
	}
	Logger.Warn(msg, args...)
}
