package relay

import (
	"bufio"
	"net"
)

type BufferedConn struct {
	net.Conn
	r *bufio.Reader
}

// Read 优先从内部 bufio.Reader 读取数据，用于复用握手阶段已读入的缓冲。
// 参数：
// - p: 目标缓冲区
// 返回：
// - int: 读取字节数
// - error: 读取错误
func (c *BufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }
