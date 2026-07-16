// Package config loads and validates the allowmaild configuration.
//
// Parsing is strict (unknown fields are rejected) and validation fails
// closed: any missing or invalid value prevents the daemon from starting.
package config

import (
	"errors"
	"fmt"
	"net/mail"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// maxSubjectBytesCap bounds max_subject_bytes so that a worst-case RFC 2047
// encoded subject still fits within the 998-octet header line limit.
const maxSubjectBytesCap = 256

var aliasPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

type Config struct {
	Sender     Sender               `yaml:"sender"`
	Recipients map[string]Recipient `yaml:"recipients"`
	Limits     Limits               `yaml:"limits"`
	SMTP       SMTP                 `yaml:"smtp"`
	SocketPath string               `yaml:"socket_path"`
	SocketMode string               `yaml:"socket_mode"`
	StateDir   string               `yaml:"state_dir"`
	// RetentionDays is how long request/audit rows are kept.
	RetentionDays int `yaml:"retention_days"`
}

type Sender struct {
	Address string `yaml:"address"`
	Name    string `yaml:"name"`
}

type Recipient struct {
	Address string           `yaml:"address"`
	Limits  *RecipientLimits `yaml:"limits"`
}

type RecipientLimits struct {
	PerHour int `yaml:"per_hour"`
	PerDay  int `yaml:"per_day"`
}

type Limits struct {
	PerHour         int `yaml:"per_hour"`
	PerDay          int `yaml:"per_day"`
	MaxSubjectBytes int `yaml:"max_subject_bytes"`
	MaxBodyBytes    int `yaml:"max_body_bytes"`
}

type SMTP struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
	// TLSMode is "implicit" (TLS from byte one, typically :465) or
	// "starttls" (plaintext greeting upgraded via STARTTLS, typically :587).
	TLSMode string `yaml:"tls_mode"`
	// Auth is "plain" or "login".
	Auth         string `yaml:"auth"`
	Username     string `yaml:"username"`
	PasswordFile string `yaml:"password_file"`
	// TimeoutSeconds bounds one whole delivery attempt.
	TimeoutSeconds int `yaml:"timeout_seconds"`
	// IOTimeoutSeconds bounds each individual read/write on the connection.
	IOTimeoutSeconds int `yaml:"io_timeout_seconds"`
}

// Load reads, strictly parses, and validates the configuration file.
// The SMTP password is read from SMTP.PasswordFile at load time so that a
// missing or unreadable secret prevents startup.
func Load(path string) (*Config, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", errors.New("config file is not readable")
	}
	defer f.Close()

	var cfg Config
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, "", fmt.Errorf("config does not parse: %v", err)
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, "", err
	}

	pw, err := os.ReadFile(cfg.SMTP.PasswordFile)
	if err != nil {
		return nil, "", errors.New("smtp.password_file is not readable")
	}
	password := strings.TrimRight(string(pw), "\r\n")
	if password == "" {
		return nil, "", errors.New("smtp.password_file is empty")
	}
	return &cfg, password, nil
}

func (c *Config) applyDefaults() {
	if c.Limits.MaxSubjectBytes == 0 {
		c.Limits.MaxSubjectBytes = 200
	}
	if c.Limits.MaxBodyBytes == 0 {
		c.Limits.MaxBodyBytes = 10000
	}
	if c.RetentionDays == 0 {
		c.RetentionDays = 90
	}
	if c.SocketMode == "" {
		c.SocketMode = "0660"
	}
	if c.SMTP.Auth == "" {
		c.SMTP.Auth = "plain"
	}
	if c.SMTP.TimeoutSeconds == 0 {
		c.SMTP.TimeoutSeconds = 60
	}
	if c.SMTP.IOTimeoutSeconds == 0 {
		c.SMTP.IOTimeoutSeconds = 20
	}
}

func (c *Config) validate() error {
	var errs []string
	fail := func(format string, args ...any) {
		errs = append(errs, fmt.Sprintf(format, args...))
	}

	if c.Sender.Address == "" {
		fail("sender.address is required")
	} else if !isBareAddress(c.Sender.Address) {
		fail("sender.address is not a valid email address")
	}

	for alias, r := range c.Recipients {
		if !aliasPattern.MatchString(alias) {
			fail("recipient alias %q is not a valid alias name", alias)
		}
		if r.Address == "" || !isBareAddress(r.Address) {
			fail("recipient %q has an invalid address", alias)
		}
		if r.Limits != nil && (r.Limits.PerHour <= 0 || r.Limits.PerDay <= 0) {
			fail("recipient %q limits must be positive", alias)
		}
	}

	if c.Limits.PerHour <= 0 {
		fail("limits.per_hour must be a positive integer")
	}
	if c.Limits.PerDay <= 0 {
		fail("limits.per_day must be a positive integer")
	}
	if c.Limits.MaxSubjectBytes <= 0 || c.Limits.MaxSubjectBytes > maxSubjectBytesCap {
		fail("limits.max_subject_bytes must be between 1 and %d", maxSubjectBytesCap)
	}
	if c.Limits.MaxBodyBytes <= 0 {
		fail("limits.max_body_bytes must be a positive integer")
	}
	if c.RetentionDays <= 0 {
		fail("retention_days must be a positive integer")
	}

	if c.SMTP.Host == "" {
		fail("smtp.host is required")
	}
	if c.SMTP.Port < 1 || c.SMTP.Port > 65535 {
		fail("smtp.port must be between 1 and 65535")
	}
	if c.SMTP.TLSMode != "implicit" && c.SMTP.TLSMode != "starttls" {
		fail("smtp.tls_mode must be \"implicit\" or \"starttls\"")
	}
	if c.SMTP.Auth != "plain" && c.SMTP.Auth != "login" {
		fail("smtp.auth must be \"plain\" or \"login\"")
	}
	if c.SMTP.Username == "" {
		fail("smtp.username is required")
	}
	if c.SMTP.PasswordFile == "" {
		fail("smtp.password_file is required")
	}
	if c.SMTP.TimeoutSeconds <= 0 || c.SMTP.IOTimeoutSeconds <= 0 {
		fail("smtp timeouts must be positive")
	}

	if c.SocketPath == "" {
		fail("socket_path is required")
	}
	if _, err := parseFileMode(c.SocketMode); err != nil {
		fail("socket_mode must be an octal file mode such as \"0660\"")
	}
	if c.StateDir == "" {
		fail("state_dir is required")
	}

	if len(errs) > 0 {
		return errors.New("invalid config: " + strings.Join(errs, "; "))
	}
	return nil
}

// isBareAddress reports whether s is a plain email address with no display
// name, group syntax, or list of addresses.
func isBareAddress(s string) bool {
	a, err := mail.ParseAddress(s)
	return err == nil && a.Name == "" && a.Address == s
}

func parseFileMode(s string) (os.FileMode, error) {
	var m uint32
	if _, err := fmt.Sscanf(s, "%o", &m); err != nil || m > 0o777 {
		return 0, errors.New("invalid mode")
	}
	return os.FileMode(m), nil
}

// SocketFileMode returns the validated socket mode.
func (c *Config) SocketFileMode() os.FileMode {
	m, _ := parseFileMode(c.SocketMode)
	return m
}

// AliasNames returns the configured alias names, sorted, for use in
// unknown-recipient errors. It never returns addresses.
func (c *Config) AliasNames() []string {
	names := make([]string, 0, len(c.Recipients))
	for a := range c.Recipients {
		names = append(names, a)
	}
	sort.Strings(names)
	return names
}

// Retention returns the configured retention period.
func (c *Config) Retention() time.Duration {
	return time.Duration(c.RetentionDays) * 24 * time.Hour
}
