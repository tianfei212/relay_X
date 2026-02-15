package control

import "relay-x/v1/config"

// Version: 1.0
// Developer: GPT-4/JOJO
// Date: 2026-02-15

// VerifyAuth 验证客户端密钥
func VerifyAuth(secret string) bool {
	// 严格模式：配置必须存在且匹配
	if config.GlobalConfig.Env.AuthSecret == "" {
		return false
	}
	return secret == config.GlobalConfig.Env.AuthSecret
}
