package control

import (
	"relay-x/v1/common"
	"time"
)

// Version: 1.0
// Developer: GPT-4/JOJO
// Date: 2026-02-15

// HandlePing 处理心跳/对时请求
// 功能说明: 记录接收时间，填充发送时间，回送 Pong 消息
func HandlePing(c *Client, payload ClockPayload) {
	// t2: 服务端接收时间
	t2 := time.Now().UnixNano()
	
	// 构造响应
	resp := ClockPayload{
		ClientSendTime: payload.ClientSendTime, // t1 原样返回
		ServerRecvTime: t2,                     // t2
	}
	
	// t3: 服务端发送时间 (尽可能接近发送时刻)
	resp.ServerSendTime = time.Now().UnixNano()
	
	// 发送 Pong
	msg := BaseMessage{
		Type:    MsgPong,
		Payload: resp,
	}
	
	if err := c.Send(msg); err != nil {
		common.LogError("发送 Pong 失败", "role", c.Role, "error", err)
	}
}
