package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"relay-x/v4/config"
	"relay-x/v4/control"
	v4log "relay-x/v4/log"
	"relay-x/v4/ports"
	"relay-x/v4/relay"
)

const Version = "1.4"

func main() {
	flag.CommandLine.SetOutput(os.Stdout)
	configPathFlag := flag.String("config_path", "configs/config.yaml", "配置文件路径（YAML）。如果是目录，则默认读取该目录下的 config.yaml")
	versionFlag := flag.Bool("version", false, "输出版本并退出")
	flag.Usage = func() {
		_, _ = fmt.Fprintf(os.Stdout, "relay-server-v4 %s\n\n", Version)
		_, _ = fmt.Fprintln(os.Stdout, "用法：")
		_, _ = fmt.Fprintln(os.Stdout, "  relay-server-v4 [--config_path <path>] [--version] [--help]")
		_, _ = fmt.Fprintln(os.Stdout, "\n参数：")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *versionFlag {
		_, _ = fmt.Fprintln(os.Stdout, Version)
		return
	}

	configPath := resolveConfigPath(*configPathFlag)
	cfg, err := config.Load(configPath)
	if err != nil {
		panic(err)
	}
	if err := v4log.Init(cfg.Logging); err != nil {
		panic(err)
	}

	zmqRange, err := config.ParsePortRange(cfg.ZeroMQ.PortRange)
	if err != nil {
		panic(err)
	}
	srtRange, err := config.ParsePortRange(cfg.GoSRT.PortRange)
	if err != nil {
		panic(err)
	}

	for p := zmqRange.Start; p <= zmqRange.End; p++ {
		if err := ports.CheckTCPPortAvailable(p); err != nil {
			v4log.With(map[string]any{"port": p, "status": "tcp_port_conflict"}).WithError(err).Error("端口占用检测失败")
			panic(err)
		}
		v4log.With(map[string]any{"port": p, "status": "tcp_port_available"}).Info("端口占用检测通过")
	}
	for p := srtRange.Start; p <= srtRange.End; p++ {
		if err := ports.CheckUDPPortAvailable(p); err != nil {
			v4log.With(map[string]any{"port": p, "status": "udp_port_conflict"}).WithError(err).Error("端口占用检测失败")
			panic(err)
		}
		v4log.With(map[string]any{"port": p, "status": "udp_port_available"}).Info("端口占用检测通过")
	}

	zmqPool, err := ports.NewPool(ports.KindZMQ, zmqRange.Start, zmqRange.End)
	if err != nil {
		panic(err)
	}
	srtPool, err := ports.NewPool(ports.KindSRT, srtRange.Start, srtRange.End)
	if err != nil {
		panic(err)
	}

	rm := relay.NewManager(cfg, zmqPool, srtPool)
	if err := rm.Start(); err != nil {
		panic(err)
	}

	ctx, cancel := signalContext()
	defer cancel()

	hub := control.NewHub(cfg, rm, zmqPool, srtPool)
	go func() {
		_ = hub.Start(ctx)
	}()

	<-ctx.Done()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.Gateway.ShutdownTimeout)
	defer shutdownCancel()
	_ = shutdownCtx
	rm.Stop()
	time.Sleep(100 * time.Millisecond)
}

func resolveConfigPath(p string) string {
	if p == "" {
		return "configs/config.yaml"
	}
	st, err := os.Stat(p)
	if err != nil {
		return p
	}
	if st.IsDir() {
		return filepath.Join(p, "config.yaml")
	}
	return p
}

// signalContext 创建一个可被 SIGINT/SIGTERM 取消的 Context。
// 返回：
// - ctx: 监听信号并在收到信号时取消的上下文
// - cancel: 主动取消函数
func signalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-ch
		cancel()
	}()
	return ctx, cancel
}
