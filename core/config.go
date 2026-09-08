package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// LoadConfig 从文件加载配置
func LoadConfig(filePath string) (*Config, error) {
	// 读取文件内容
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return nil, fmt.Errorf("%w: null config", ErrInvalidConfig)
	}

	// 创建默认配置
	config := DefaultConfig()

	// 根据文件扩展名选择解析方式
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".json":
		err = json.Unmarshal(data, config)
	default:
		err = json.Unmarshal(data, config)
	}

	if err != nil {
		return nil, err
	}

	if err := config.Validate(); err != nil {
		return nil, err
	}
	return config, nil
}

// SaveConfig 保存配置到文件。
// 先写入同目录临时文件再原子替换，避免写坏已有配置
func SaveConfig(config *Config, filePath string) error {
	if err := config.Validate(); err != nil {
		return err
	}
	// 根据文件扩展名选择序列化方式
	ext := strings.ToLower(filepath.Ext(filePath))
	var data []byte
	var err error

	switch ext {
	case ".json":
		data, err = json.MarshalIndent(config, "", "  ")
	default:
		data, err = json.MarshalIndent(config, "", "  ")
	}

	if err != nil {
		return err
	}
	data = append(data, '\n')

	// 确保目录存在
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	return os.Rename(tmpName, filePath)
}

// LoadConfigFromEnv 从环境变量加载配置。
// 环境变量解析失败时返回带字段信息的错误，而不是静默忽略
func LoadConfigFromEnv(config *Config) error {
	if config == nil {
		return errors.New("LoadConfigFromEnv: config is nil")
	}
	original := config
	copyConfig := *config
	config = &copyConfig

	if host := os.Getenv("BINGO_HOST"); host != "" {
		config.Host = host
	}

	if port := os.Getenv("BINGO_PORT"); port != "" {
		p, err := strconv.Atoi(port)
		if err != nil {
			return fmt.Errorf("LoadConfigFromEnv: invalid BINGO_PORT %q: %w", port, err)
		}
		config.Port = p
	}

	if runMode := os.Getenv("BINGO_RUN_MODE"); runMode != "" {
		config.RunMode = RunMode(runMode)
	}

	if logLevel := os.Getenv("BINGO_LOG_LEVEL"); logLevel != "" {
		config.LogLevel = logLevel
	}

	if v := os.Getenv("BINGO_READ_TIMEOUT"); v != "" {
		t, err := parseTimeoutEnv("BINGO_READ_TIMEOUT", v)
		if err != nil {
			return err
		}
		config.ReadTimeout = t
	}

	if v := os.Getenv("BINGO_WRITE_TIMEOUT"); v != "" {
		t, err := parseTimeoutEnv("BINGO_WRITE_TIMEOUT", v)
		if err != nil {
			return err
		}
		config.WriteTimeout = t
	}

	if v := os.Getenv("BINGO_IDLE_TIMEOUT"); v != "" {
		t, err := parseTimeoutEnv("BINGO_IDLE_TIMEOUT", v)
		if err != nil {
			return err
		}
		config.IdleTimeout = t
	}

	if maxBodySize := os.Getenv("BINGO_MAX_BODY_SIZE"); maxBodySize != "" {
		s, err := strconv.Atoi(maxBodySize)
		if err != nil {
			return fmt.Errorf("LoadConfigFromEnv: invalid BINGO_MAX_BODY_SIZE %q: %w", maxBodySize, err)
		}
		config.MaxRequestBodySize = s
	}

	if serverName := os.Getenv("BINGO_SERVER_NAME"); serverName != "" {
		config.ServerName = serverName
	}

	if err := config.Validate(); err != nil {
		return err
	}
	*original = *config
	return nil
}

// parseTimeoutEnv 解析秒为单位的超时环境变量
func parseTimeoutEnv(name, value string) (time.Duration, error) {
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("LoadConfigFromEnv: invalid %s %q: %w", name, value, err)
	}
	if seconds < 0 || seconds > int64((1<<63-1)/time.Second) {
		return 0, fmt.Errorf("%w: %s out of range", ErrInvalidConfig, name)
	}
	return time.Duration(seconds) * time.Second, nil
}

// Validate is shared by file/environment loading and checked construction.
// Zero timeouts intentionally disable the corresponding timeout.
func (c *Config) Validate() error {
	invalid := func(field string) error { return fmt.Errorf("%w: %s", ErrInvalidConfig, field) }
	if c == nil {
		return invalid("nil config")
	}
	if c.Host == "" {
		return invalid("host is empty")
	}
	if c.Port < 1 || c.Port > 65535 {
		return invalid("port must be 1..65535")
	}
	if c.ReadTimeout < 0 || c.WriteTimeout < 0 || c.IdleTimeout < 0 {
		return invalid("timeouts must be non-negative")
	}
	if c.MaxRequestBodySize <= 0 {
		return invalid("max_request_body_size must be positive")
	}
	if c.RunMode != RunModeDebug && c.RunMode != RunModeRelease && c.RunMode != RunModeTest {
		return invalid("run_mode")
	}
	if !isKnownLogLevel(c.LogLevel) {
		return invalid("log_level")
	}
	if c.MultiCore.NumCPU < 0 || c.MultiCore.NumCPU > 1<<31-1 {
		return invalid("multi_core.NumCPU")
	}
	if c.MultiCore.MaxConns <= 0 || c.MultiCore.ReadBufferSize <= 0 || c.MultiCore.WriteBufferSize <= 0 {
		return invalid("multi_core limits must be positive")
	}
	return nil
}

// NewAppChecked reports invalid input instead of applying compatibility defaults.
func NewAppChecked(config *Config) (*App, error) {
	if config == nil {
		config = DefaultConfig()
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return NewApp(config), nil
}

// ProductionConfig is an explicit preset. NewApp never changes resource limits
// merely because RunMode is release. Override this preset before construction.
func ProductionConfig() *Config {
	c := DefaultConfig()
	c.RunMode = RunModeRelease
	c.ReadTimeout = 15 * time.Second
	c.WriteTimeout = 15 * time.Second
	c.IdleTimeout = 30 * time.Second
	c.MultiCore.ReadBufferSize = 8192
	c.MultiCore.WriteBufferSize = 8192
	c.MultiCore.MaxConns = 50000
	c.MaxRequestBodySize = 16 << 20
	c.ServerName = "Bingo-Production"
	c.LogLevel = "warn"
	return c
}
