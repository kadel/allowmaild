package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	pwPath := filepath.Join(dir, "password")
	if err := os.WriteFile(pwPath, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	content = strings.ReplaceAll(content, "PASSWORD_FILE", pwPath)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const validConfig = `
sender:
  address: sender@example.test
  name: Sender Name
recipients:
  self-gmail:
    address: dest@example.test
limits:
  per_hour: 5
  per_day: 20
smtp:
  host: smtp.example.test
  port: 465
  tls_mode: implicit
  username: sender@example.test
  password_file: PASSWORD_FILE
socket_path: /run/allowmail/allowmail.sock
state_dir: /var/lib/allowmaild
`

func TestValidConfigLoads(t *testing.T) {
	cfg, pw, err := Load(writeConfig(t, validConfig))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if pw != "s3cret" {
		t.Errorf("password = %q (trailing newline should be stripped)", pw)
	}
	if cfg.Limits.MaxSubjectBytes != 200 || cfg.Limits.MaxBodyBytes != 10000 {
		t.Errorf("byte limit defaults not applied: %+v", cfg.Limits)
	}
	if cfg.RetentionDays != 90 {
		t.Errorf("retention default = %d, want 90", cfg.RetentionDays)
	}
	if got := cfg.AliasNames(); len(got) != 1 || got[0] != "self-gmail" {
		t.Errorf("AliasNames = %v", got)
	}
}

func TestUnknownFieldRejected(t *testing.T) {
	_, _, err := Load(writeConfig(t, validConfig+"\nunknown_thing: 1\n"))
	if err == nil {
		t.Fatal("config with unknown field loaded; want strict-parse failure")
	}
}

func TestMissingFileFails(t *testing.T) {
	if _, _, err := Load(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Fatal("missing config file loaded")
	}
}

func TestInvalidConfigsFailClosed(t *testing.T) {
	cases := map[string]string{
		"no sender":         strings.Replace(validConfig, "address: sender@example.test", "address: \"\"", 1),
		"bad recipient":     strings.Replace(validConfig, "address: dest@example.test", "address: not-an-address", 1),
		"bad alias name":    strings.Replace(validConfig, "self-gmail:", "\"Self Gmail!\":", 1),
		"zero per_hour":     strings.Replace(validConfig, "per_hour: 5", "per_hour: 0", 1),
		"bad tls mode":      strings.Replace(validConfig, "tls_mode: implicit", "tls_mode: none", 1),
		"no socket":         strings.Replace(validConfig, "socket_path: /run/allowmail/allowmail.sock", "socket_path: \"\"", 1),
		"no state dir":      strings.Replace(validConfig, "state_dir: /var/lib/allowmaild", "state_dir: \"\"", 1),
		"huge subject max":  validConfig + "\n", // placeholder replaced below
	}
	cases["huge subject max"] = strings.Replace(validConfig, "per_day: 20", "per_day: 20\n  max_subject_bytes: 5000", 1)

	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := Load(writeConfig(t, content)); err == nil {
				t.Fatalf("invalid config (%s) loaded; want failure", name)
			}
		})
	}
}

func TestMissingPasswordFileFailsStartup(t *testing.T) {
	path := writeConfig(t, validConfig)
	// Point at a nonexistent secret.
	raw, _ := os.ReadFile(path)
	content := strings.ReplaceAll(string(raw), filepath.Dir(path), filepath.Join(filepath.Dir(path), "missing"))
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(path); err == nil {
		t.Fatal("config with unreadable password_file loaded")
	}
}

func TestErrorsDoNotEchoPaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("::::not yaml"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := Load(path)
	if err == nil {
		t.Fatal("want error")
	}
	if strings.Contains(err.Error(), dir) {
		t.Fatalf("error echoes filesystem path: %v", err)
	}
}
