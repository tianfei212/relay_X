package ports

import (
	"fmt"
	"net"
	"time"
)

// CheckTCPPortAvailable 检测 TCP 端口是否可用（通过尝试监听并立即关闭）。
// 参数：
// - port: 端口号
// 返回：
// - error: 端口不可用或监听失败原因
func CheckTCPPortAvailable(port int) error {
	ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return err
	}
	_ = ln.Close()
	return nil
}

// CheckUDPPortAvailable 检测 UDP 端口是否可用（通过尝试绑定并立即关闭）。
// 参数：
// - port: 端口号
// 返回：
// - error: 端口不可用或绑定失败原因
func CheckUDPPortAvailable(port int) error {
	addr := &net.UDPAddr{IP: net.IPv4zero, Port: port}
	c, err := net.ListenUDP("udp", addr)
	if err != nil {
		return err
	}
	_ = c.SetDeadline(time.Now())
	_ = c.Close()
	return nil
}
