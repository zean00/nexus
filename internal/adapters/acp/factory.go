package acp

import (
	"strings"
	"time"

	"nexus/internal/ports"
)

type BridgeConfig struct {
	Implementation   string
	BaseURL          string
	Token            string
	Command          string
	Args             []string
	Env              []string
	Workdir          string
	DefaultAgentName string
	StartupTimeout   time.Duration
	RPCTimeout       time.Duration
	Headers          map[string]string
}

func NewBridge(cfg BridgeConfig) ports.ACPBridge {
	switch strings.ToLower(strings.TrimSpace(cfg.Implementation)) {
	case "stdio":
		return NewStdioClient(StdioConfig{
			Command:          cfg.Command,
			Args:             cfg.Args,
			Env:              cfg.Env,
			Workdir:          cfg.Workdir,
			DefaultAgentName: cfg.DefaultAgentName,
			StartupTimeout:   cfg.StartupTimeout,
			RPCTimeout:       cfg.RPCTimeout,
		})
	case "sse":
		client := NewSSEClient(cfg.BaseURL, cfg.Token)
		client.Headers = cfg.Headers
		return client
	case "strict", "acp", "native":
		client := NewStrictClient(cfg.BaseURL, cfg.Token)
		client.Headers = cfg.Headers
		return client
	default:
		client := New(cfg.BaseURL, cfg.Token)
		client.Headers = cfg.Headers
		return client
	}
}
