package control

import "relay-x/v1/config"

// VerifyAuth 验证客户端密钥
func VerifyAuth(secret string) bool {
	if config.GlobalConfig.Env.AuthSecret == "" {
		return false
	}
	return secret == config.GlobalConfig.Env.AuthSecret
}
