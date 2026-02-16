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
	tcpConn  net.Conn
	udpConn  *net.UDPConn
	port     int
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
func NewDataClient(port int, role, clientID string) *DataClient {
	return &DataClient{
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
		return fmt.Errorf("连接 TCP 端口 %d 失败: %v", c.port, err)
	}

	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("解析 UDP 地址失败: %v", err)
	}
	udpConn, err := net.DialUDP("udp", nil, udpAddr)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("连接 UDP 端口 %d 失败: %v", c.port, err)
	}

	c.tcpConn = conn
	c.udpConn = udpConn

	h, _ := json.Marshal(map[string]string{
		"role":      c.role,
		"client_id": c.clientID,
	})
	_, _ = c.tcpConn.Write(append(append([]byte("RLX1HELLO "), h...), '\n'))
	_, _ = c.udpConn.Write(append([]byte("RLX1HELLO_UDP "), h...))

	fmt.Printf("[data-%d] 已连接 tcp+udp\n", c.port)
	return nil
}

// StartEchoLoop 启动回传循环
func (c *DataClient) StartEchoLoop() {
	go c.readAndEchoTCP()
	go c.readAndEchoUDP()
}

func (c *DataClient) readAndEchoTCP() {
	defer c.Close()

	buf := utils.GetBuffer()
	defer utils.PutBuffer(buf)

	for {
		n, err := c.tcpConn.Read(buf)
		if err != nil {
			if !c.isClosed() {
				fmt.Printf("[data-%d][tcp] 读取失败: %v\n", c.port, err)
			}
			return
		}

		if n > 0 {
			atomic.AddInt64(&c.bytesReceived, int64(n))

			// 立即回传
			if _, err := c.tcpConn.Write(buf[:n]); err != nil {
				if !c.isClosed() {
					fmt.Printf("[data-%d][tcp] 写入失败: %v\n", c.port, err)
				}
				return
			}

			atomic.AddInt64(&c.bytesSent, int64(n))
		}
	}
}

func (c *DataClient) readAndEchoUDP() {
	defer c.Close()

	buf := make([]byte, 64*1024)
	for {
		n, err := c.udpConn.Read(buf)
		if err != nil {
			if !c.isClosed() {
				fmt.Printf("[data-%d][udp] 读取失败: %v\n", c.port, err)
			}
			return
		}
		if n > 0 {
			atomic.AddInt64(&c.bytesReceived, int64(n))
			if _, err := c.udpConn.Write(buf[:n]); err != nil {
				if !c.isClosed() {
					fmt.Printf("[data-%d][udp] 写入失败: %v\n", c.port, err)
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
		if c.tcpConn != nil {
			_ = c.tcpConn.Close()
		}
		if c.udpConn != nil {
			_ = c.udpConn.Close()
		}
		fmt.Printf("[data-%d] 已关闭\n", c.port)
	}
}

// isClosed 检查是否已关闭
func (c *DataClient) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func (c *DataClient) Port() int {
	return c.port
}
