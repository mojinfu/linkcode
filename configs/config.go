// Package configs defines the LinkCode configuration structure and loading.
package configs

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the root configuration for a LinkCode instance.
type Config struct {
	DB         DBConfig      `yaml:"db"`
	ControlBot BotCredential `yaml:"control_bot"`
	Agent      AgentConfig   `yaml:"agent"`
	Admin      AdminConfig   `yaml:"admin"`
	EncryptKey string        `yaml:"encrypt_key"`
}

// DBConfig holds MySQL connection parameters.
type DBConfig struct {
	DSN          string `yaml:"dsn"`
	MaxOpenConns int    `yaml:"max_open_conns"`
	MaxIdleConns int    `yaml:"max_idle_conns"`
}

// BotCredential holds the botId and secret for a WeCom AI Bot.
type BotCredential struct {
	BotID  string `yaml:"bot_id"`
	Secret string `yaml:"secret"`
}

// AgentConfig holds agent-related settings.
type AgentConfig struct {
	DefaultType    string        `yaml:"default_type"`
	IdleTimeout    time.Duration `yaml:"idle_timeout"`
	ClaudeCodePath string        `yaml:"claude_code_path"`
	ClaudeWorkDir  string        `yaml:"claude_work_dir"`
}

// AdminConfig holds admin panel settings.
type AdminConfig struct {
	Enabled  bool   `yaml:"enabled"`
	BindAddr string `yaml:"bind_addr"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		DB: DBConfig{
			DSN:          "root:@tcp(127.0.0.1:3306)/linkcode?parseTime=true&multiStatements=true",
			MaxOpenConns: 10,
			MaxIdleConns: 5,
		},
		Agent: AgentConfig{
			DefaultType:    "claude-code",
			IdleTimeout:    30 * time.Minute,
			ClaudeCodePath: "claude",
			ClaudeWorkDir:  "",
		},
		Admin: AdminConfig{
			Enabled:  true,
			BindAddr: "127.0.0.1:18980",
		},
	}
}

// Load reads a YAML config file and merges with defaults.
func Load(path string) (Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("configs: read %s: %w", path, err)
	}

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("configs: parse %s: %w", path, err)
	}

	// Override with environment variables (highest priority).
	if v := os.Getenv("LINKCODE_DB_DSN"); v != "" {
		cfg.DB.DSN = v
	}
	if v := os.Getenv("LINKCODE_CONTROL_BOT_ID"); v != "" {
		cfg.ControlBot.BotID = v
	}
	if v := os.Getenv("LINKCODE_CONTROL_BOT_SECRET"); v != "" {
		cfg.ControlBot.Secret = v
	}
	if v := os.Getenv("LINKCODE_ENCRYPT_KEY"); v != "" {
		cfg.EncryptKey = v
	}

	// Fallback: load encryption key from local file if not in config.
	if cfg.EncryptKey == "" {
		keyFile := "configs/.encrypt_key"
		if keyBytes, err := os.ReadFile(keyFile); err == nil {
			cfg.EncryptKey = string(keyBytes)
		}
	}

	return cfg, nil
}
