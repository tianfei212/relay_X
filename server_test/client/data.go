package client

import (
	"encoding/json"
	"fmt"
	"net"
	"server_test/utils"
	"sync"
	"sync/atomic"
)

// Version: 1.0
// Developer: GPT-4/JOJO
// Date: 2026-02-15

// DataClient 数据面客户端
type DataClient struct {
	conn     net.Conn
	port     int
	protocol string // "srt" 或 "zmq"
	host     string
	role     string
	clientID string

	// 统计
	bytesReceived int64
	bytesSent     int64

	// 控制
	mu     sync.Mutex
	closed bool
}

// NewDataClient 创建新的数据面客户端
func NewDataClient(protocol string, port int, role, clientID string) *DataClient {
	return &DataClient{
		protocol: protocol,
		port:     port,
		role:     role,
		clientID: clientID,
	}
}

func (c *DataClient) SetHost(host string) {
	c.host = host
}

// Connect 连接到网关的数据端口
func (c *DataClient) Connect() error {
	host := c.host
	if host == "" {
		host = "localhost"
	}
	addr := fmt.Sprintf("%s:%d", host, c.port)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return fmt.Errorf("连接 %s 端口 %d 失败: %v", c.protocol, c.port, err)
	}

	c.conn = conn
	h, _ := json.Marshal(map[string]string{
		"role":      c.role,
		"client_id": c.clientID,
	})
	_, _ = c.conn.Write(append(append([]byte("RLX1HELLO "), h...), '\n'))
	fmt.Printf("[%s-%d] 已连接\n", c.protocol, c.port)
	return nil
}

// StartEchoLoop 启动回传循环
func (c *DataClient) StartEchoLoop() {
	go c.readAndEcho()
}

// readAndEcho 读取数据并回传
func (c *DataClient) readAndEcho() {
	defer c.Close()

	buf := utils.GetBuffer()
	defer utils.PutBuffer(buf)

	for {
		n, err := c.conn.Read(buf)
		if err != nil {
			if !c.isClosed() {
				fmt.Printf("[%s-%d] 读取失败: %v\n", c.protocol, c.port, err)
			}
			return
		}

		if n > 0 {
			atomic.AddInt64(&c.bytesReceived, int64(n))

			// 立即回传
			if _, err := c.conn.Write(buf[:n]); err != nil {
				if !c.isClosed() {
					fmt.Printf("[%s-%d] 写入失败: %v\n", c.protocol, c.port, err)
				}
				return
			}

			atomic.AddInt64(&c.bytesSent, int64(n))
		}
	}
}

// GetStats 获取统计信息
func (c *DataClient) GetStats() (received, sent int64) {
	return atomic.LoadInt64(&c.bytesReceived), atomic.LoadInt64(&c.bytesSent)
}

// Close 关闭连接
func (c *DataClient) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.closed {
		c.closed = true
		if c.conn != nil {
			c.conn.Close()
		}
		fmt.Printf("[%s-%d] 已关闭\n", c.protocol, c.port)
	}
}

// isClosed 检查是否已关闭
func (c *DataClient) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func (c *DataClient) Protocol() string {
	return c.protocol
}

func (c *DataClient) Port() int {
	return c.port
}
