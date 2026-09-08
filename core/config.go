package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// LoadConfig 从 JSON 文件加载配置；文件未提供的字段保留默认值。
// 时间字段沿用 time.Duration 的 JSON 表示，单位为纳秒。
func LoadConfig(filePath string) (*Config, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read configuration %q: %w", filePath, err)
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return nil, fmt.Errorf("%w: configuration must be a JSON object", ErrInvalidConfig)
	}
	config := DefaultConfig()
	if err := json.Unmarshal(data, config); err != nil {
		return nil, fmt.Errorf("%w: decode configuration %q: %w", ErrInvalidConfig, filePath, err)
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return config, nil
}

// SaveConfig 将配置完整写入同目录的临时文件，同步并关闭后再替换目标文件。
// 替换失败时保留旧文件并清理临时文件；已有文件的权限会被保留。
func SaveConfig(config *Config, filePath string) error {
	if err := config.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("encode configuration: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create configuration directory: %w", err)
	}
	mode := os.FileMode(0600)
	if info, err := os.Stat(filePath); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%w: configuration target %q is not a regular file", ErrInvalidConfig, filePath)
		}
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect configuration target: %w", err)
	}
	file, err := os.CreateTemp(dir, "."+filepath.Base(filePath)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary configuration: %w", err)
	}
	tempPath := file.Name()
	defer func() {
		file.Close()
		// Windows 拒绝删除只读文件；继承只读权限后替换失败也必须能清理临时文件。
		os.Chmod(tempPath, 0600)
		os.Remove(tempPath)
	}()
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write temporary configuration: %w", err)
	}
	if err := file.Chmod(mode); err != nil {
		return fmt.Errorf("set configuration permissions: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync temporary configuration: %w", err)
	}
	// Windows 不允许依赖一个仍打开的临时文件完成替换，因此先检查 Close 的错误。
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temporary configuration: %w", err)
	}
	if err := replaceConfigFile(tempPath, filePath); err != nil {
		return fmt.Errorf("replace configuration %q: %w", filePath, err)
	}
	return nil
}

// LoadConfigFromEnv 从环境变量加载配置。超时单位为秒，0 表示禁用该超时。
// 所有字段验证通过后才写回 config，防止失败时留下半更新状态。
func LoadConfigFromEnv(config *Config) error {
	if config == nil {
		return fmt.Errorf("%w: configuration is nil", ErrInvalidConfig)
	}
	next := *config
	if host := os.Getenv("BINGO_HOST"); host != "" {
		next.Host = host
	}
	if raw := os.Getenv("BINGO_PORT"); raw != "" {
		port, err := strconv.Atoi(raw)
		if err != nil || port < 1 || port > 65535 {
			return invalidEnvValue("BINGO_PORT", "must be an integer between 1 and 65535")
		}
		next.Port = port
	}
	if runMode := os.Getenv("BINGO_RUN_MODE"); runMode != "" {
		switch RunMode(runMode) {
		case RunModeDebug, RunModeRelease, RunModeTest:
			next.RunMode = RunMode(runMode)
		default:
			return invalidEnvValue("BINGO_RUN_MODE", "must be debug, release, or test")
		}
	}
	if level := os.Getenv("BINGO_LOG_LEVEL"); level != "" {
		if !isKnownLogLevel(level) {
			return invalidEnvValue("BINGO_LOG_LEVEL", "must be debug, info, warn, error, or fatal")
		}
		next.LogLevel = level
	}
	for _, field := range []struct {
		name string
		dest *time.Duration
	}{
		{"BINGO_READ_TIMEOUT", &next.ReadTimeout},
		{"BINGO_WRITE_TIMEOUT", &next.WriteTimeout},
		{"BINGO_IDLE_TIMEOUT", &next.IdleTimeout},
	} {
		if raw := os.Getenv(field.name); raw != "" {
			seconds, err := strconv.ParseInt(raw, 10, 64)
			// 检查乘法前的上限，避免大数乘 time.Second 溢出为错误的超时值。
			if err != nil || seconds < 0 || seconds > int64((1<<63-1)/time.Second) {
				return invalidEnvValue(field.name, "must be a non-negative integer number of seconds within time.Duration range")
			}
			*field.dest = time.Duration(seconds) * time.Second
		}
	}
	if raw := os.Getenv("BINGO_MAX_BODY_SIZE"); raw != "" {
		size, err := strconv.Atoi(raw)
		if err != nil || size <= 0 {
			return invalidEnvValue("BINGO_MAX_BODY_SIZE", "must be a positive integer number of bytes")
		}
		next.MaxRequestBodySize = size
	}
	if name := os.Getenv("BINGO_SERVER_NAME"); name != "" {
		next.ServerName = name
	}
	if err := next.Validate(); err != nil {
		return err
	}
	*config = next
	return nil
}

func invalidEnvValue(name, message string) error {
	return fmt.Errorf("%w: %s %s", ErrInvalidConfig, name, message)
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
