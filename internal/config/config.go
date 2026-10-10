// Package config loads and saves pulse's settings and resolves the server
// password from the environment, the config file or the OS keyring.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/zalando/go-keyring"
)

const (
	appName        = "pulse"
	keyringService = "pulse"
	// PULSE_* is PulseAudio's namespace (PULSE_SERVER is set by WSLg and
	// many desktops), so pulse uses PULSETUI_*.
	passwordEnvVar  = "PULSETUI_PASSWORD"
	serverEnvVar    = "PULSETUI_SERVER"
	usernameEnvVar  = "PULSETUI_USERNAME"
	defaultBitRate  = 320
	defaultVolume   = 80
	fileHeader      = "# pulse configuration - see config.example.toml for all options.\n\n"
	filePermissions = 0o600
)

// Config is the on-disk configuration.
type Config struct {
	ServerURL string `toml:"server_url"`
	Username  string `toml:"username"`
	// Password is optional; the OS keyring or PULSETUI_PASSWORD is preferred.
	Password string `toml:"password,omitempty"`
	// AlwaysTranscode makes the server send MP3 for every track.
	AlwaysTranscode bool `toml:"always_transcode"`
	// MaxBitRate (kbps) used when the server transcodes.
	MaxBitRate int `toml:"max_bitrate"`
	// Volume (0-100) restored at startup; updated on exit.
	Volume int `toml:"volume"`
	// InsecureSkipVerify disables TLS verification (self-signed certs).
	InsecureSkipVerify bool `toml:"insecure_skip_verify"`
	// OpaqueBackground paints the Catppuccin Mocha background colours instead
	// of letting the terminal's own (possibly translucent) background show.
	OpaqueBackground bool `toml:"opaque_background"`

	// Source is the library shown at startup: "navidrome" or "local".
	// Updated on exit to whichever was last active.
	Source string `toml:"source"`
	// MusicFolder is the local library folder.
	MusicFolder string `toml:"music_folder"`
	// FFmpegPath points at ffmpeg for formats pulse can't decode itself
	// (AAC/M4A, ALAC, Opus, WMA, ...). Empty: look for ffmpeg on PATH.
	FFmpegPath string `toml:"ffmpeg_path"`
}

// Library sources.
const (
	SourceNavidrome = "navidrome"
	SourceLocal     = "local"
)

// DefaultPath returns the platform config location, e.g.
// %AppData%\pulse\config.toml on Windows or ~/.config/pulse/config.toml.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, appName, "config.toml"), nil
}

// Load reads the config at path. A missing file yields defaults and
// os.ErrNotExist. Environment variables override file values.
func Load(path string) (*Config, error) {
	cfg := &Config{MaxBitRate: defaultBitRate, Volume: defaultVolume}
	_, err := toml.DecodeFile(path, cfg)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if v := os.Getenv(serverEnvVar); v != "" {
		cfg.ServerURL = v
	}
	if v := os.Getenv(usernameEnvVar); v != "" {
		cfg.Username = v
	}
	cfg.normalize()
	return cfg, err
}

func (c *Config) normalize() {
	c.ServerURL = strings.TrimRight(strings.TrimSpace(c.ServerURL), "/")
	if c.MaxBitRate <= 0 {
		c.MaxBitRate = defaultBitRate
	}
	c.Volume = max(0, min(100, c.Volume))
	if c.Source != SourceLocal {
		c.Source = SourceNavidrome
	}
}

// HasServer reports whether a Navidrome server is configured.
func (c *Config) HasServer() bool {
	return c.ServerURL != "" && c.Username != ""
}

// Configured reports whether any music source has been set up.
func (c *Config) Configured() bool {
	return c.HasServer() || c.MusicFolder != "" || c.Source == SourceLocal
}

// Save writes the config to path, creating the directory if needed.
func (c *Config) Save(path string) error {
	c.normalize()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	var buf bytes.Buffer
	buf.WriteString(fileHeader)
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), filePermissions)
}

func (c *Config) keyringUser() string {
	return c.Username + "@" + c.ServerURL
}

// ResolvePassword returns the password from, in order: PULSETUI_PASSWORD,
// the config file, the OS keyring.
func (c *Config) ResolvePassword() (string, error) {
	if v := os.Getenv(passwordEnvVar); v != "" {
		return v, nil
	}
	if c.Password != "" {
		return c.Password, nil
	}
	pw, err := keyring.Get(keyringService, c.keyringUser())
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", fmt.Errorf("no password stored for %s; run `pulse --setup` or set %s", c.keyringUser(), passwordEnvVar)
		}
		return "", fmt.Errorf("reading password from OS keyring: %w (set %s instead)", err, passwordEnvVar)
	}
	return pw, nil
}

// StorePassword saves the password in the OS keyring.
func (c *Config) StorePassword(pw string) error {
	return keyring.Set(keyringService, c.keyringUser(), pw)
}
