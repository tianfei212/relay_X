package log

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"relay-x/v4/config"

	"github.com/sirupsen/logrus"
	"gopkg.in/natefinch/lumberjack.v2"
)

var base = logrus.New()

// Init 初始化 V4 日志系统。
// 参数：
// - cfg: 日志配置（级别、输出、格式与文件滚动策略）
// 返回：
// - error: 初始化失败原因（如文件目录无法创建）
func Init(cfg config.LoggingConfig) error {
	level, err := logrus.ParseLevel(strings.ToLower(cfg.Level))
	if err != nil {
		level = logrus.InfoLevel
	}
	base.SetLevel(level)
	base.SetReportCaller(false)

	if strings.ToLower(cfg.Format) == "json" {
		base.SetFormatter(&logrus.JSONFormatter{
			TimestampFormat: "2006-01-02T15:04:05.000Z07:00",
		})
	} else {
		base.SetFormatter(&logrus.TextFormatter{
			FullTimestamp:   true,
			TimestampFormat: "2006-01-02T15:04:05.000Z07:00",
		})
	}

	switch strings.ToLower(cfg.Output) {
	case "console":
		base.SetOutput(os.Stdout)
	case "file":
		if err := os.MkdirAll(filepath.Dir(cfg.FilePath), 0o755); err != nil {
			return err
		}
		base.SetOutput(&lumberjack.Logger{
			Filename:   cfg.FilePath,
			MaxSize:    max(1, int(cfg.MaxSize.Int64()/(1024*1024))),
			MaxAge:     max(1, cfg.MaxAge),
			Compress:   cfg.Compress,
			MaxBackups: 3,
			LocalTime:  true,
		})
	default:
		base.SetOutput(os.Stdout)
	}

	base.AddHook(runtimeHook{})
	return nil
}

// L 返回底层 logrus Logger 指针（全局单例）。
func L() *logrus.Logger { return base }

// With 创建带字段的日志 Entry。
// 参数：
// - fields: 结构化字段
func With(fields logrus.Fields) *logrus.Entry { return base.WithFields(fields) }

type runtimeHook struct{}

// Levels 返回 Hook 适用的日志级别集合。
func (h runtimeHook) Levels() []logrus.Level { return logrus.AllLevels }

// Fire 在日志输出前补齐 goid/func/ts_ms 字段（若未显式设置）。
func (h runtimeHook) Fire(e *logrus.Entry) error {
	if _, ok := e.Data["goid"]; !ok {
		e.Data["goid"] = goid()
	}
	if _, ok := e.Data["func"]; !ok {
		if pc, _, _, ok := runtime.Caller(8); ok {
			if fn := runtime.FuncForPC(pc); fn != nil {
				e.Data["func"] = fn.Name()
			}
		}
	}
	if _, ok := e.Data["ts_ms"]; !ok {
		e.Data["ts_ms"] = time.Now().UnixMilli()
	}
	return nil
}

// goid 解析当前 goroutine ID（仅用于日志辅助字段）。
func goid() int64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	s := strings.TrimPrefix(string(buf[:n]), "goroutine ")
	i := strings.IndexByte(s, ' ')
	if i < 0 {
		return 0
	}
	id, _ := strconv.ParseInt(s[:i], 10, 64)
	return id
}

// max 返回两个整数中的较大值。
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
