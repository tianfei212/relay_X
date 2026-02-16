package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"relay-x/v4/control"
	"relay-x/v4/relay"
)

// main 启动 V4 混沌测试工具。
// 使用说明：
// - 启动大量 client 注册到控制面并发起配对
// - 注入随机延迟与随机丢弃（drop_pct），模拟不稳定网络与异常行为
func main() {
	addr := flag.String("addr", "127.0.0.1:5555", "control address")
	clients := flag.Int("clients", 100, "clients")
	duration := flag.Duration("duration", 30*time.Second, "duration")
	delayMax := flag.Duration("delay_max", 200*time.Millisecond, "max injected delay")
	dropPct := flag.Int("drop_pct", 5, "random drop percent (0-100)")
	flag.Parse()

	rand.Seed(time.Now().UnixNano())

	var okPairs atomic.Int64
	var errPairs atomic.Int64
	stopAt := time.Now().Add(*duration)

	var wg sync.WaitGroup
	for i := 0; i < *clients; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			runChaosClient(*addr, fmt.Sprintf("chaos-%d", i), stopAt, *delayMax, *dropPct, &okPairs, &errPairs)
		}(i)
	}

	wg.Wait()
	fmt.Printf("pairs_ok=%d pairs_err=%d\n", okPairs.Load(), errPairs.Load())
}

// runChaosClient 模拟不稳定 client：随机延迟/丢弃配对请求，并进行短连接数据面拨测。
func runChaosClient(addr, id string, stopAt time.Time, delayMax time.Duration, dropPct int, okPairs, errPairs *atomic.Int64) {
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

	host, _, _ := net.SplitHostPort(addr)

	for time.Now().Before(stopAt) {
		if rand.Intn(100) < dropPct {
			time.Sleep(time.Duration(rand.Int63n(int64(delayMax))))
			continue
		}
		time.Sleep(time.Duration(rand.Int63n(int64(delayMax))))
		reqID := fmt.Sprintf("r-%d", time.Now().UnixNano())
		_ = enc.Encode(control.Envelope{Type: control.MsgPairRequest, Payload: control.PairRequestPayload{RequestID: reqID, NeedZMQ: true}})
		var env control.Envelope
		if err := dec.Decode(&env); err != nil {
			return
		}
		if env.Type != control.MsgPairGrant {
			errPairs.Add(1)
			continue
		}
		okPairs.Add(1)
		var grant control.PairGrantPayload
		b, _ := json.Marshal(env.Payload)
		_ = json.Unmarshal(b, &grant)

		if grant.ZMQPort > 0 && rand.Intn(100) >= dropPct {
			d, err := net.Dial("tcp", fmt.Sprintf("%s:%d", host, grant.ZMQPort))
			if err == nil {
				_, _ = d.Write([]byte(fmt.Sprintf("%s{\"role\":\"client\",\"session_id\":\"%s\",\"id\":\"%s\"}\n", relay.HelloPrefix, grant.SessionID, id)))
				_ = d.SetDeadline(time.Now().Add(500 * time.Millisecond))
				_ = d.Close()
			}
		}
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
