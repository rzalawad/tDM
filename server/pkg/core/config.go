package core

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// Environment represents the application environment
type Environment string

const (
	EnvDevelopment Environment = "development"
	EnvTesting     Environment = "testing"
	EnvProduction  Environment = "production"
)

// ServerConfig contains server-specific configuration
type ServerConfig struct {
	Host string `yaml:"host" json:"host"`
	Port int    `yaml:"port" json:"port"`
}

// Validate validates the server configuration
func (c *ServerConfig) Validate() error {
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("invalid port number: %d", c.Port)
	}
	return nil
}

// Aria2Config contains configuration for launching aria2c
type Aria2Config struct {
	Port            int               `yaml:"port" json:"port"`
	Secret          string            `yaml:"secret" json:"secret,omitempty"`
	Log             string            `yaml:"log" json:"log"`
	DownloadOptions map[string]string `yaml:"download_options" json:"download_options,omitempty"`
}

// BuildCommand builds the command arguments for launching aria2c
func (c *Aria2Config) BuildCommand() []string {
	cmd := []string{
		"aria2c",
		"--enable-rpc",
		"--daemon=true",
		"--rpc-listen-all=true",
		"--rpc-allow-origin-all",
		fmt.Sprintf("--log=%s", c.Log),
		fmt.Sprintf("--rpc-listen-port=%d", c.Port),
	}

	if c.Secret != "" {
		cmd = append(cmd, fmt.Sprintf("--rpc-secret=%s", c.Secret))
	}

	return cmd
}

// DaemonConfig contains daemon-specific configuration
type DaemonConfig struct {
	Concurrency                int               `yaml:"concurrency" json:"concurrency"`
	ExpireDownloads            string            `yaml:"expire_downloads" json:"expire_downloads"`
	Mapper                     map[string]string `yaml:"mapper" json:"mapper,omitempty"`
	TemporaryDownloadDirectory string            `yaml:"temporary_download_directory" json:"temporary_download_directory,omitempty"`
	Organize                   string            `yaml:"organize" json:"organize,omitempty"`
	Aria2                      Aria2Config       `yaml:"aria2" json:"aria2"`
}

// Validate validates the daemon configuration
func (c *DaemonConfig) Validate() error {
	if c.Concurrency < 1 {
		return fmt.Errorf("invalid concurrency value: %d", c.Concurrency)
	}

	// Validate temporary download directory if provided
	if c.TemporaryDownloadDirectory != "" {
		parent := filepath.Dir(c.TemporaryDownloadDirectory)
		if _, err := os.Stat(parent); os.IsNotExist(err) {
			log.Printf("Warning: Parent directory of temporary_download_directory does not exist: %s", parent)
		}
	}

	return nil
}

// LoggingConfig contains logging-specific configuration
type LoggingConfig struct {
	Level      string `yaml:"level" json:"level"`
	Format     string `yaml:"format" json:"format"`
	DateFormat string `yaml:"date_format" json:"date_format"`
}

// Validate validates the logging configuration
func (c *LoggingConfig) Validate() error {
	validLevels := []string{"DEBUG", "INFO", "WARNING", "ERROR", "CRITICAL"}
	level := strings.ToUpper(c.Level)

	valid := false
	for _, l := range validLevels {
		if level == l {
			valid = true
			break
		}
	}

	if !valid {
		return fmt.Errorf("invalid log level: %s. Must be one of %v", c.Level, validLevels)
	}

	return nil
}

// GetLogLevel returns the log level as an integer
func (c *LoggingConfig) GetLogLevel() string {
	return strings.ToUpper(c.Level)
}

// AppConfig represents the main application configuration
type AppConfig struct {
	Environment  Environment   `yaml:"environment" json:"environment"`
	DatabasePath string        `yaml:"database_path" json:"database_path"`
	Server       ServerConfig  `yaml:"server" json:"server"`
	Daemon       DaemonConfig  `yaml:"daemon" json:"daemon"`
	Logging      LoggingConfig `yaml:"logging" json:"logging"`
}

// Validate validates the entire configuration
func (c *AppConfig) Validate() error {
	if err := c.Server.Validate(); err != nil {
		return fmt.Errorf("server config validation failed: %w", err)
	}

	if err := c.Daemon.Validate(); err != nil {
		return fmt.Errorf("daemon config validation failed: %w", err)
	}

	if err := c.Logging.Validate(); err != nil {
		return fmt.Errorf("logging config validation failed: %w", err)
	}

	// Validate database path
	dbPath := filepath.Dir(c.DatabasePath)
	if _, err := os.Stat(dbPath); os.IsNotExist(err) && dbPath != "." {
		log.Printf("Warning: Parent directory for database does not exist: %s", dbPath)
	}

	return nil
}

// ConfigManager manages application configuration
type ConfigManager struct {
	config *AppConfig
	mu     sync.RWMutex
}

var configManager *ConfigManager
var once sync.Once

// GetConfigManager returns the singleton config manager instance
func GetConfigManager() *ConfigManager {
	once.Do(func() {
		configManager = &ConfigManager{}
	})
	return configManager
}

// LoadConfig loads configuration from file and environment variables
func (cm *ConfigManager) LoadConfig(configPath string) (*AppConfig, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Start with default configuration
	config := &AppConfig{
		Environment:  EnvDevelopment,
		DatabasePath: "downloads.db",
		Server: ServerConfig{
			Host: "0.0.0.0",
			Port: 54759,
		},
		Daemon: DaemonConfig{
			Concurrency:     1,
			ExpireDownloads: "1d",
			Mapper:          make(map[string]string),
			Aria2: Aria2Config{
				Port:            6800,
				Log:             "/tmp/aria2.log",
				DownloadOptions: make(map[string]string),
			},
		},
		Logging: LoggingConfig{
			Level:      "INFO",
			Format:     "%(asctime)s - %(name)s - %(levelname)s - %(message)s",
			DateFormat: "2006-01-02 15:04:05",
		},
	}

	// Override with environment variable for environment
	if env := os.Getenv("TDM_APP_ENV"); env != "" {
		config.Environment = Environment(env)
	}

	// Override with environment variable for log level
	if level := os.Getenv("LOG_LEVEL"); level != "" {
		config.Logging.Level = level
	}

	// Try to get config path from environment if not provided
	if configPath == "" {
		configPath = os.Getenv("TDM_CONFIG_PATH")
	}

	// Load configuration from file if it exists
	if configPath != "" {
		if _, err := os.Stat(configPath); err == nil {
			log.Printf("Loading configuration from file: %s", configPath)

			data, err := os.ReadFile(configPath)
			if err != nil {
				return nil, fmt.Errorf("error reading config file: %w", err)
			}

			var fileConfig AppConfig
			if err := yaml.Unmarshal(data, &fileConfig); err != nil {
				return nil, fmt.Errorf("error parsing config file: %w", err)
			}

			// Override defaults with file values (simplified, not a deep merge)
			if fileConfig.Environment != "" {
				config.Environment = fileConfig.Environment
			}

			if fileConfig.DatabasePath != "" {
				config.DatabasePath = fileConfig.DatabasePath
			}

			// Server config
			if fileConfig.Server.Host != "" {
				config.Server.Host = fileConfig.Server.Host
			}
			if fileConfig.Server.Port != 0 {
				config.Server.Port = fileConfig.Server.Port
			}

			// Daemon config
			if fileConfig.Daemon.Concurrency != 0 {
				config.Daemon.Concurrency = fileConfig.Daemon.Concurrency
			}
			if fileConfig.Daemon.ExpireDownloads != "" {
				config.Daemon.ExpireDownloads = fileConfig.Daemon.ExpireDownloads
			}
			if fileConfig.Daemon.TemporaryDownloadDirectory != "" {
				config.Daemon.TemporaryDownloadDirectory = fileConfig.Daemon.TemporaryDownloadDirectory
			}
			if len(fileConfig.Daemon.Mapper) > 0 {
				config.Daemon.Mapper = fileConfig.Daemon.Mapper
			}
			if fileConfig.Daemon.Organize != "" {
				config.Daemon.Organize = fileConfig.Daemon.Organize
			}

			// Aria2 config
			if fileConfig.Daemon.Aria2.Port != 0 {
				config.Daemon.Aria2.Port = fileConfig.Daemon.Aria2.Port
			}
			if fileConfig.Daemon.Aria2.Secret != "" {
				config.Daemon.Aria2.Secret = fileConfig.Daemon.Aria2.Secret
			}
			if fileConfig.Daemon.Aria2.Log != "" {
				config.Daemon.Aria2.Log = fileConfig.Daemon.Aria2.Log
			}
			if len(fileConfig.Daemon.Aria2.DownloadOptions) > 0 {
				config.Daemon.Aria2.DownloadOptions = fileConfig.Daemon.Aria2.DownloadOptions
			}

			// Logging config
			if fileConfig.Logging.Level != "" {
				config.Logging.Level = fileConfig.Logging.Level
			}
			if fileConfig.Logging.Format != "" {
				config.Logging.Format = fileConfig.Logging.Format
			}
			if fileConfig.Logging.DateFormat != "" {
				config.Logging.DateFormat = fileConfig.Logging.DateFormat
			}
		}
	}

	// Validate the configuration
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("configuration validation failed: %w", err)
	}

	cm.config = config
	return config, nil
}

// GetConfig returns the current configuration
func (cm *ConfigManager) GetConfig() (*AppConfig, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	if cm.config == nil {
		return nil, fmt.Errorf("configuration not loaded, call LoadConfig first")
	}

	return cm.config, nil
}

// InitializeConfig initializes configuration from config file
func InitializeConfig(configPath string) (*AppConfig, error) {
	cm := GetConfigManager()
	config, err := cm.LoadConfig(configPath)
	if err != nil {
		return nil, err
	}

	// Configure logging based on config
	// This would typically set up a logging framework
	log.Printf("Application initialized in %s environment", config.Environment)

	return config, nil
}

// GetConfig is a convenience function to get the current configuration
func GetConfig() (*AppConfig, error) {
	cm := GetConfigManager()
	return cm.GetConfig()
}
