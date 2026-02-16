package relay

import (
	"bufio"
	"net"
)

type BufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *BufferedConn) Read(p []byte) (int, error) {
	return c.r.Read(p)
}
