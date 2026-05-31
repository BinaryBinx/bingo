package core

import (
	"encoding/json"
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

	return config, nil
}

// SaveConfig 保存配置到文件
func SaveConfig(config *Config, filePath string) error {
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

	// 确保目录存在
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	// 写入文件
	return os.WriteFile(filePath, data, 0644)
}

// LoadConfigFromEnv 从环境变量加载配置
func LoadConfigFromEnv(config *Config) error {
	if config == nil {
		return nil
	}

	if host := os.Getenv("BINGO_HOST"); host != "" {
		config.Host = host
	}

	if port := os.Getenv("BINGO_PORT"); port != "" {
		if p, err := strconv.Atoi(port); err == nil {
			config.Port = p
		}
	}

	if runMode := os.Getenv("BINGO_RUN_MODE"); runMode != "" {
		config.RunMode = RunMode(runMode)
	}

	if logLevel := os.Getenv("BINGO_LOG_LEVEL"); logLevel != "" {
		config.LogLevel = logLevel
	}

	if readTimeout := os.Getenv("BINGO_READ_TIMEOUT"); readTimeout != "" {
		if t, err := strconv.Atoi(readTimeout); err == nil {
			config.ReadTimeout = time.Duration(t) * time.Second
		}
	}

	if writeTimeout := os.Getenv("BINGO_WRITE_TIMEOUT"); writeTimeout != "" {
		if t, err := strconv.Atoi(writeTimeout); err == nil {
			config.WriteTimeout = time.Duration(t) * time.Second
		}
	}

	if idleTimeout := os.Getenv("BINGO_IDLE_TIMEOUT"); idleTimeout != "" {
		if t, err := strconv.Atoi(idleTimeout); err == nil {
			config.IdleTimeout = time.Duration(t) * time.Second
		}
	}

	if maxBodySize := os.Getenv("BINGO_MAX_BODY_SIZE"); maxBodySize != "" {
		if s, err := strconv.Atoi(maxBodySize); err == nil {
			config.MaxRequestBodySize = s
		}
	}

	if serverName := os.Getenv("BINGO_SERVER_NAME"); serverName != "" {
		config.ServerName = serverName
	}

	return nil
}
