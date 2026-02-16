package control

import (
	"relay-x/v1/common"
	"time"
)

func HandlePing(c *Client, payload ClockPayload) {
	t2 := time.Now().UnixNano()

	resp := ClockPayload{
		ClientSendTime: payload.ClientSendTime,
		ServerRecvTime: t2,
	}
	resp.ServerSendTime = time.Now().UnixNano()

	msg := BaseMessage{
		Type:    MsgPong,
		Payload: resp,
	}
	if err := c.Send(msg); err != nil {
		common.LogError("发送 Pong 失败", "role", c.Role, "error", err)
	}
}
