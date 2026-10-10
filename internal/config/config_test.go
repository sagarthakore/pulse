package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestLoadMissingReturnsDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want ErrNotExist", err)
	}
	if cfg.MaxBitRate != 320 || cfg.Volume != 80 || cfg.HasServer() || cfg.Configured() || cfg.Source != SourceNavidrome {
		t.Fatalf("defaults = %+v", cfg)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.toml")
	in := &Config{ServerURL: "https://music.example.com/", Username: "alice", MaxBitRate: 192, Volume: 55, AlwaysTranscode: true, Source: SourceLocal, MusicFolder: `D:\Music`}
	if err := in.Save(path); err != nil {
		t.Fatal(err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if out.ServerURL != "https://music.example.com" || out.Username != "alice" ||
		out.MaxBitRate != 192 || out.Volume != 55 || !out.AlwaysTranscode || out.Password != "" ||
		out.Source != SourceLocal || out.MusicFolder != `D:\Music` {
		t.Fatalf("round trip = %+v", out)
	}
}

func TestEnvOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := (&Config{ServerURL: "http://a", Username: "file", Password: "frompw"}).Save(path); err != nil {
		t.Fatal(err)
	}
	t.Setenv(serverEnvVar, "http://b:4533")
	t.Setenv(usernameEnvVar, "env")
	t.Setenv(passwordEnvVar, "envpw")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServerURL != "http://b:4533" || cfg.Username != "env" {
		t.Fatalf("env not applied: %+v", cfg)
	}
	if pw, err := cfg.ResolvePassword(); err != nil || pw != "envpw" {
		t.Fatalf("ResolvePassword = %q, %v", pw, err)
	}
}

func TestConfigPasswordUsedBeforeKeyring(t *testing.T) {
	t.Setenv(passwordEnvVar, "")
	cfg := &Config{ServerURL: "http://a", Username: "u", Password: "inline"}
	if pw, err := cfg.ResolvePassword(); err != nil || pw != "inline" {
		t.Fatalf("ResolvePassword = %q, %v", pw, err)
	}
}

func TestPasswordFromKeyring(t *testing.T) {
	keyring.MockInit()
	t.Setenv(passwordEnvVar, "")
	cfg := &Config{ServerURL: "http://nas:4533", Username: "alice"}

	if _, err := cfg.ResolvePassword(); err == nil {
		t.Fatal("expected an error before a password is stored")
	}
	if err := cfg.StorePassword("secret"); err != nil {
		t.Fatal(err)
	}
	if pw, err := cfg.ResolvePassword(); err != nil || pw != "secret" {
		t.Fatalf("ResolvePassword = %q, %v", pw, err)
	}
}
