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
	case "strict", "acp", "native":
		return NewStrictClient(cfg.BaseURL, cfg.Token)
	default:
		return New(cfg.BaseURL, cfg.Token)
	}
}
