package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"time"

	srt "github.com/datarhei/gosrt"

	"relay-x/v4/control"
	"relay-x/v4/relay"
)

// main 启动 V4 后端（server 端）模拟器。
// 使用说明：
// - 连接控制平面注册为 RoleServer
// - 收到 pair_grant 后，按下发端口连接数据面并发送 V4HELLO
// - 数据面收到数据后原样回写（echo），用于联调验证
func main() {
	addr := flag.String("addr", "127.0.0.1:5555", "control address")
	id := flag.String("id", "be1", "server id")
	token := flag.String("token", "t", "server token")
	maxConn := flag.Int("max", 100, "max sessions")
	flag.Parse()

	conn, err := net.Dial("tcp", *addr)
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)

	_ = enc.Encode(control.Envelope{Type: control.MsgRegister, Payload: control.RegisterPayload{
		Role:    control.RoleServer,
		ID:      *id,
		Token:   *token,
		MaxConn: *maxConn,
	}})
	expect(dec, control.MsgRegisterAck)

	host, _, _ := net.SplitHostPort(*addr)

	for {
		var env control.Envelope
		if err := dec.Decode(&env); err != nil {
			return
		}
		switch env.Type {
		case control.MsgPairGrant:
			var grant control.PairGrantPayload
			b, _ := json.Marshal(env.Payload)
			_ = json.Unmarshal(b, &grant)
			fmt.Printf("pair_grant session=%s zmq=%d srt=%d client=%s\n", grant.SessionID, grant.ZMQPort, grant.SRTPort, grant.ClientID)
			if grant.ZMQPort > 0 {
				c, err := net.Dial("tcp", fmt.Sprintf("%s:%d", host, grant.ZMQPort))
				if err == nil {
					writeHello(c, "server", grant.SessionID, *id)
					go echoLoop(c, "tcp")
				}
			}
			if grant.SRTPort > 0 {
				scfg := srt.DefaultConfig()
				c, err := srt.Dial("srt", fmt.Sprintf("%s:%d", host, grant.SRTPort), scfg)
				if err == nil {
					writeHello(c, "server", grant.SessionID, *id)
					go echoLoop(c, "srt")
				}
			}
		case control.MsgStatus:
			_ = json.NewEncoder(os.Stdout).Encode(env)
		}
		time.Sleep(0)
	}
}

// expect 读取直到出现指定类型的控制面消息。
func expect(dec *json.Decoder, typ control.MessageType) control.Envelope {
	for {
		var env control.Envelope
		if err := dec.Decode(&env); err != nil {
			panic(err)
		}
		if env.Type == typ {
			return env
		}
	}
}

// writeHello 向数据面连接写入 V4HELLO 握手行。
func writeHello(c net.Conn, role, sessionID, id string) {
	payload := fmt.Sprintf("%s{\"role\":\"%s\",\"session_id\":\"%s\",\"id\":\"%s\"}\n", relay.HelloPrefix, role, sessionID, id)
	_, _ = c.Write([]byte(payload))
}

// echoLoop 将读到的数据原样写回（用于联调验证数据通路）。
func echoLoop(c net.Conn, tag string) {
	defer c.Close()
	r := bufio.NewReader(c)
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if err != nil {
			return
		}
		if n > 0 {
			_, _ = c.Write(buf[:n])
		}
		_ = tag
	}
}
