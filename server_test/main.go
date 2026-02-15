package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"server_test/client"
	"server_test/config"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Version: 1.0
// Developer: GPT-4/JOJO
// Date: 2026-02-15

func main() {
	if err := config.Load("./config.yaml"); err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	fmt.Println("后端程序启动...")
	fmt.Printf("网关地址: %s\n", config.GlobalConfig.GatewayAddr)
	fmt.Printf("后端ID: %s\n", config.GlobalConfig.BEID)

	controlClient := client.NewControlClient()

	if err := controlClient.Connect(); err != nil {
		log.Fatalf("连接网关失败: %v", err)
	}
	defer controlClient.Close()

	if err := controlClient.Register(); err != nil {
		log.Fatalf("注册失败: %v", err)
	}

	if err := controlClient.WaitForAuth(); err != nil {
		log.Fatalf("认证失败: %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	if err := controlClient.SendPortAllocation(); err != nil {
		log.Fatalf("发送端口分配请求失败: %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	if err := controlClient.SendKeyExchange(); err != nil {
		log.Fatalf("发送密钥交换失败: %v", err)
	}

	stopCh := make(chan struct{})
	go controlClient.StartListening()
	go controlClient.StartPingLoop(1*time.Second, stopCh)

	var clientsMu sync.Mutex
	var dataClients []*client.DataClient
	connected := make(map[int]bool)
	connectedByProto := map[string]int{"zmq": 0, "srt": 0}
	dataHost := strings.TrimSpace(config.GlobalConfig.DataHost)
	if dataHost == "" {
		if parts := strings.Split(config.GlobalConfig.GatewayAddr, ":"); len(parts) > 0 && parts[0] != "" {
			dataHost = parts[0]
		}
	}
	connectOne := func(proto string, port int) {
		if port <= 0 {
			return
		}
		limit := 2
		if proto == "zmq" && config.GlobalConfig.ZMQConnLimit > 0 {
			limit = config.GlobalConfig.ZMQConnLimit
		}
		if proto == "srt" && config.GlobalConfig.SRTConnLimit > 0 {
			limit = config.GlobalConfig.SRTConnLimit
		}
		clientsMu.Lock()
		if connected[port] {
			clientsMu.Unlock()
			return
		}
		if connectedByProto[proto] >= limit {
			clientsMu.Unlock()
			return
		}
		connected[port] = true
		connectedByProto[proto]++
		clientsMu.Unlock()

		id := config.GlobalConfig.BEID + "-" + proto
		dc := client.NewDataClient(proto, port, "BE", id)
		dc.SetHost(dataHost)
		if err := connectWithRetry(dc, 60, 50*time.Millisecond); err != nil {
			fmt.Printf("[%s-%d] 连接失败: %v\n", proto, port, err)
			clientsMu.Lock()
			delete(connected, port)
			connectedByProto[proto]--
			clientsMu.Unlock()
			return
		}
		dc.StartEchoLoop()
		clientsMu.Lock()
		dataClients = append(dataClients, dc)
		clientsMu.Unlock()
	}

	go func() {
		for g := range controlClient.PortGrantChan() {
			if g.Error != "" {
				continue
			}
			if g.ZMQPort > 0 {
				connectOne("zmq", g.ZMQPort)
			}
			if g.SRTPort > 0 {
				connectOne("srt", g.SRTPort)
			}
		}
	}()

	go func() {
		for s := range controlClient.PortStateChan() {
			for _, p := range s.ZMQEmptyPorts {
				connectOne("zmq", p)
			}
			for _, p := range s.SRTEmptyPorts {
				connectOne("srt", p)
			}
		}
	}()

	go startDataPlaneTelemetry(func() []*client.DataClient {
		clientsMu.Lock()
		defer clientsMu.Unlock()
		out := make([]*client.DataClient, len(dataClients))
		copy(out, dataClients)
		return out
	})

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	<-sigChan
	close(stopCh)
	fmt.Println("\n收到中断信号，正在关闭...")
}

func connectWithRetry(dc *client.DataClient, attempts int, delay time.Duration) error {
	var last error
	for i := 0; i < attempts; i++ {
		if err := dc.Connect(); err == nil {
			return nil
		} else {
			last = err
			time.Sleep(delay)
		}
	}
	return last
}

func startDataPlaneTelemetry(getClients func() []*client.DataClient) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	type snap struct {
		rx int64
		tx int64
	}
	last := make(map[*client.DataClient]snap)

	for {
		<-ticker.C
		for _, dc := range getClients() {
			rx, tx := dc.GetStats()
			prev := last[dc]
			drx := rx - prev.rx
			dtx := tx - prev.tx
			last[dc] = snap{rx: rx, tx: tx}

			rxMbps := float64(drx*8) / 1e6
			txMbps := float64(dtx*8) / 1e6

			fmt.Printf("[%s-%d][DATA] rx=%.2fMbps tx=%.2fMbps total_rx=%d total_tx=%d\n",
				dc.Protocol(), dc.Port(), rxMbps, txMbps, rx, tx)
		}
	}
}
