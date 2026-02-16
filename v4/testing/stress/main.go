package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"relay-x/v4/control"
	"relay-x/v4/relay"
)

// main 启动 V4 压力测试工具。
// 使用说明：
// - 启动若干 server 注册到控制面
// - 启动若干 client 持续发起 pair_request
// - 输出配对成功/失败计数
func main() {
	addr := flag.String("addr", "127.0.0.1:5555", "control address")
	servers := flag.Int("servers", 10, "servers")
	clients := flag.Int("clients", 50, "clients")
	needZMQ := flag.Bool("zmq", true, "need zmq")
	needSRT := flag.Bool("srt", false, "need srt")
	duration := flag.Duration("duration", 30*time.Second, "duration")
	flag.Parse()

	var okPairs atomic.Int64
	var errPairs atomic.Int64

	var wg sync.WaitGroup
	for i := 0; i < *servers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			runServer(*addr, fmt.Sprintf("srv-%d", i))
		}(i)
	}

	time.Sleep(200 * time.Millisecond)

	stopAt := time.Now().Add(*duration)
	for i := 0; i < *clients; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			runClient(*addr, fmt.Sprintf("cli-%d", i), *needZMQ, *needSRT, stopAt, &okPairs, &errPairs)
		}(i)
	}

	wg.Wait()
	fmt.Printf("pairs_ok=%d pairs_err=%d\n", okPairs.Load(), errPairs.Load())
}

// runServer 模拟一个后端 server：注册后等待 pair_grant 并连接数据端口。
func runServer(addr, id string) {
	c, err := net.Dial("tcp", addr)
	if err != nil {
		return
	}
	defer c.Close()
	enc := json.NewEncoder(c)
	dec := json.NewDecoder(c)

	_ = enc.Encode(control.Envelope{Type: control.MsgRegister, Payload: control.RegisterPayload{Role: control.RoleServer, ID: id, Token: "t", MaxConn: 1000}})
	if !waitType(dec, control.MsgRegisterAck) {
		return
	}
	host, _, _ := net.SplitHostPort(addr)
	for {
		var env control.Envelope
		if err := dec.Decode(&env); err != nil {
			return
		}
		if env.Type != control.MsgPairGrant {
			continue
		}
		var grant control.PairGrantPayload
		b, _ := json.Marshal(env.Payload)
		_ = json.Unmarshal(b, &grant)
		if grant.ZMQPort > 0 {
			d, err := net.Dial("tcp", fmt.Sprintf("%s:%d", host, grant.ZMQPort))
			if err == nil {
				_, _ = d.Write([]byte(fmt.Sprintf("%s{\"role\":\"server\",\"session_id\":\"%s\",\"id\":\"%s\"}\n", relay.HelloPrefix, grant.SessionID, id)))
				go echo(d)
			}
		}
	}
}

// runClient 模拟一个前端 client：持续请求配对并统计成功/失败。
func runClient(addr, id string, needZMQ, needSRT bool, stopAt time.Time, okPairs, errPairs *atomic.Int64) {
	c, err := net.Dial("tcp", addr)
	if err != nil {
		return
	}
	defer c.Close()
	enc := json.NewEncoder(c)
	dec := json.NewDecoder(c)

	_ = enc.Encode(control.Envelope{Type: control.MsgRegister, Payload: control.RegisterPayload{Role: control.RoleClient, ID: id, Token: "t", MaxConn: 1}})
	if !waitType(dec, control.MsgRegisterAck) {
		return
	}

	for time.Now().Before(stopAt) {
		reqID := fmt.Sprintf("r-%d", time.Now().UnixNano())
		_ = enc.Encode(control.Envelope{Type: control.MsgPairRequest, Payload: control.PairRequestPayload{RequestID: reqID, NeedZMQ: needZMQ, NeedSRT: needSRT}})
		var env control.Envelope
		if err := dec.Decode(&env); err != nil {
			return
		}
		if env.Type == control.MsgPairGrant {
			okPairs.Add(1)
		} else {
			errPairs.Add(1)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitType 读取控制面消息直到出现指定类型，连接断开则返回 false。
func waitType(dec *json.Decoder, typ control.MessageType) bool {
	for {
		var env control.Envelope
		if err := dec.Decode(&env); err != nil {
			return false
		}
		if env.Type == typ {
			return true
		}
	}
}

// echo 将读到的数据原样写回（用于占用数据通路并制造流量）。
func echo(c net.Conn) {
	defer c.Close()
	buf := make([]byte, 4096)
	for {
		n, err := c.Read(buf)
		if err != nil {
			return
		}
		if n > 0 {
			_, _ = c.Write(buf[:n])
		}
	}
}
