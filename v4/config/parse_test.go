package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// TestParsePortRange 验证端口范围字符串解析行为。
func TestParsePortRange(t *testing.T) {
	r, err := ParsePortRange("2300-2320")
	if err != nil {
		t.Fatal(err)
	}
	if r.Start != 2300 || r.End != 2320 {
		t.Fatalf("bad range: %+v", r)
	}
	if _, err := ParsePortRange("bad"); err == nil {
		t.Fatalf("expected error")
	}
}

// TestByteSizeUnmarshal 验证 ByteSize 支持从 YAML 文本解析（如 100MB）。
func TestByteSizeUnmarshal(t *testing.T) {
	var cfg struct {
		Size ByteSize `yaml:"size"`
	}
	if err := yaml.Unmarshal([]byte("size: 100MB\n"), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Size.Int64() != 100*1024*1024 {
		t.Fatalf("got=%d", cfg.Size.Int64())
	}
}
