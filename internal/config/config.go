package config

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// [Config]
type Config struct {
	IMAPServer          string
	IMAPUser            string
	IMAPPassword        string
	DataDir             string
	RulesDir            string
	HTTPPort            int
	APIKey              string
	IdleRefreshInterval time.Duration
}

// [Loader]
func Load() (*Config, error) {
	cfg := &Config{
		IMAPServer:          "imap.gmail.com:993",
		DataDir:             "./data",
		HTTPPort:            8080,
		IdleRefreshInterval: 15 * time.Minute,
	}

	// Environment variables
	if v := os.Getenv("IMAP_SERVER"); v != "" {
		cfg.IMAPServer = v
	}
	if v := os.Getenv("IMAP_USER"); v != "" {
		cfg.IMAPUser = v
	}
	if v := os.Getenv("IMAP_PASSWORD"); v != "" {
		cfg.IMAPPassword = v
	}
	if v := os.Getenv("DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
	if v := os.Getenv("HTTP_PORT"); v != "" {
		if port, err := strconv.Atoi(v); err == nil && port > 0 {
			cfg.HTTPPort = port
		}
	}
	if v := os.Getenv("RULES_DIR"); v != "" {
		cfg.RulesDir = v
	}
	if v := os.Getenv("API_KEY"); v != "" {
		cfg.APIKey = v
	}

	// Command-line flags
	var (
		fs           = flag.NewFlagSet("gmail-archiver", flag.ContinueOnError)
		imapServer   = fs.String("imap-server", cfg.IMAPServer, "IMAP server address host:port")
		imapUser     = fs.String("imap-user", cfg.IMAPUser, "IMAP username or email")
		imapPassword = fs.String("imap-password", cfg.IMAPPassword, "IMAP app password")
		dataDir      = fs.String("data-dir", cfg.DataDir, "Data storage directory")
		rulesDir     = fs.String("rules-dir", cfg.RulesDir, "Assignment rules directory (default: <data-dir>/rules)")
		httpPort     = fs.Int("http-port", cfg.HTTPPort, "HTTP server listening port")
		apiKey       = fs.String("api-key", cfg.APIKey, "API key for authentication (optional)")
	)

	if err := fs.Parse(os.Args[1:]); err != nil {
		return nil, err
	}

	cfg.IMAPServer = strings.TrimSpace(*imapServer)
	cfg.IMAPUser = strings.TrimSpace(*imapUser)
	cfg.IMAPPassword = strings.TrimSpace(*imapPassword)
	cfg.DataDir = strings.TrimSpace(*dataDir)
	cfg.RulesDir = strings.TrimSpace(*rulesDir)
	if cfg.RulesDir == "" {
		cfg.RulesDir = filepath.Join(cfg.DataDir, "rules")
	}
	cfg.HTTPPort = *httpPort
	cfg.APIKey = strings.TrimSpace(*apiKey)

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// [Validation]
func (c *Config) Validate() error {
	if c.IMAPUser == "" {
		return errors.New("IMAP_USER or -imap-user is required")
	}
	if c.IMAPPassword == "" {
		return errors.New("IMAP_PASSWORD or -imap-password is required")
	}
	if c.IMAPServer == "" {
		return errors.New("IMAP_SERVER is required")
	}
	if c.HTTPPort <= 0 || c.HTTPPort > 65535 {
		return fmt.Errorf("invalid HTTP port: %d", c.HTTPPort)
	}
	return nil
}
