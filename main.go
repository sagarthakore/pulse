// Command pulse is a terminal music player for Navidrome (and other
// Subsonic-compatible servers) and music folders on your computer.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"

	"github.com/sagarthakore/pulse/internal/config"
	"github.com/sagarthakore/pulse/internal/local"
	"github.com/sagarthakore/pulse/internal/player"
	"github.com/sagarthakore/pulse/internal/subsonic"
	"github.com/sagarthakore/pulse/internal/ui"
)

// version is set by release builds (-ldflags "-X main.version=..."). When it
// isn't, appVersion falls back to the module version Go records in the
// binary, e.g. v0.1.0 for `go install github.com/sagarthakore/pulse@v0.1.0`.
var version = "dev"

func appVersion() string {
	if version != "dev" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return version
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "pulse:", err)
		os.Exit(1)
	}
}

func run() error {
	setup := flag.Bool("setup", false, "(re)configure the Navidrome server connection and exit")
	cfgPath := flag.String("config", "", "path to config file (default: OS config dir)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("pulse", appVersion())
		return nil
	}

	path := *cfgPath
	if path == "" {
		p, err := config.DefaultPath()
		if err != nil {
			return err
		}
		path = p
	}

	cfg, err := config.Load(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if *setup || !cfg.Configured() {
		if err := runSetup(cfg, path, !*setup); err != nil {
			return err
		}
		if *setup {
			return nil
		}
	}

	engine, err := player.New(player.Options{Volume: cfg.Volume})
	if err != nil {
		return err
	}
	defer engine.Close()

	opts := ui.Options{OpaqueBackground: cfg.OpaqueBackground, Start: ui.SourceNavidrome}
	if cfg.Source == config.SourceLocal {
		opts.Start = ui.SourceLocal
	}

	if cfg.HasServer() {
		nav, label, err := connectNavidrome(cfg)
		if err != nil {
			opts.NavidromeErr = err.Error()
			opts.Notice = fmt.Sprintf("couldn't connect to %s: %v (run pulse --setup to change it)", cfg.ServerURL, err)
		} else {
			opts.Navidrome, opts.NavidromeLabel = nav, label
		}
	}

	localOpts, err := localOptions(cfg, path)
	if err != nil {
		return err
	}
	if cfg.MusicFolder != "" {
		lib, err := local.Open(cfg.MusicFolder, localOpts)
		if err != nil {
			opts.Notice = fmt.Sprintf("music folder unavailable: %v (press O to choose another)", err)
		} else {
			opts.Local = lib
		}
	}
	opts.ChooseFolder = func(root string) (*local.Library, error) {
		lib, err := local.Open(root, localOpts)
		if err != nil {
			return nil, err
		}
		cfg.MusicFolder = lib.Root()
		if err := cfg.Save(path); err != nil {
			return nil, fmt.Errorf("saving config: %w", err)
		}
		return lib, nil
	}

	model := ui.New(engine, opts)
	prog := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := prog.Run(); err != nil {
		return err
	}

	source := config.SourceNavidrome
	if model.ActiveSource() == ui.SourceLocal {
		source = config.SourceLocal
	}
	if v := model.Volume(); v != cfg.Volume || source != cfg.Source {
		cfg.Volume, cfg.Source = v, source
		if err := cfg.Save(path); err != nil {
			fmt.Fprintln(os.Stderr, "pulse: saving settings:", err)
		}
	}
	return nil
}

// navidrome adapts the Subsonic client to the UI's Library interface,
// deciding per track whether the server should transcode.
type navidrome struct {
	*subsonic.Client
	alwaysTranscode bool
	maxBitRate      int
}

func (n navidrome) Open(ctx context.Context, m player.Media) (player.Source, error) {
	format, bitRate, suffix := "raw", 0, strings.ToLower(m.Suffix)
	if n.alwaysTranscode || !player.CanDecode(suffix) {
		format, bitRate, suffix = "mp3", n.maxBitRate, "mp3"
	}
	resp, err := n.Stream(ctx, m.ID, subsonic.StreamOptions{Format: format, MaxBitRate: bitRate})
	if err != nil {
		return player.Source{}, err
	}
	return player.Source{Body: resp.Body, Size: resp.ContentLength, Format: suffix}, nil
}

// connectNavidrome logs in to the configured server.
func connectNavidrome(cfg *config.Config) (ui.Library, string, error) {
	password, err := cfg.ResolvePassword()
	if err != nil {
		return nil, "", err
	}
	client := subsonic.New(cfg.ServerURL, cfg.Username, password, subsonic.Options{
		InsecureSkipVerify: cfg.InsecureSkipVerify,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	server, err := client.Ping(ctx)
	if err != nil {
		return nil, "", err
	}
	label := cfg.Username + " @ " + hostOf(cfg.ServerURL) + " · " + server
	return navidrome{Client: client, alwaysTranscode: cfg.AlwaysTranscode, maxBitRate: cfg.MaxBitRate}, label, nil
}

// localOptions places the scan cache in the OS cache dir and favorites/play
// counts next to the config file.
func localOptions(cfg *config.Config, configPath string) (local.Options, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return local.Options{}, err
	}
	return local.Options{
		FFmpeg:   local.FindFFmpeg(cfg.FFmpegPath),
		CacheDir: filepath.Join(cacheDir, "pulse"),
		DataDir:  filepath.Dir(configPath),
	}, nil
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	return u.Host
}

// runSetup interactively asks for Navidrome connection details, verifies
// them and saves them. On first run (firstRun) an empty server URL skips
// Navidrome and starts pulse with local music only.
func runSetup(cfg *config.Config, path string, firstRun bool) error {
	in := bufio.NewReader(os.Stdin)
	if firstRun {
		fmt.Println("Welcome to pulse — it plays music from a Navidrome server and/or a folder on this computer.")
		fmt.Println()
	} else {
		fmt.Println("pulse setup — connect to your Navidrome / Subsonic server")
		fmt.Println()
	}

	for {
		label := "Server URL (e.g. http://192.168.1.10:4533 or https://music.example.com)"
		if firstRun {
			label = "Navidrome server URL (leave empty to only play local music)"
		}
		serverURL := prompt(in, label, cfg.ServerURL)
		if serverURL == "" {
			if !firstRun {
				fmt.Println("  a server URL is required")
				continue
			}
			cfg.Source = config.SourceLocal
			if err := cfg.Save(path); err != nil {
				return fmt.Errorf("saving config: %w", err)
			}
			fmt.Println("No server — pulse will start with local music; choose your music folder when it opens.")
			fmt.Println()
			return nil
		}
		if !strings.Contains(serverURL, "://") {
			serverURL = "https://" + serverURL
			fmt.Println("  no scheme given, using", serverURL)
		}
		username := prompt(in, "Username", cfg.Username)
		password, err := promptPassword(in, "Password")
		if err != nil {
			return err
		}

		cfg.ServerURL = strings.TrimRight(serverURL, "/")
		cfg.Username = username

		fmt.Print("Testing connection… ")
		client := subsonic.New(cfg.ServerURL, username, password, subsonic.Options{InsecureSkipVerify: cfg.InsecureSkipVerify})
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		server, err := client.Ping(ctx)
		cancel()
		if err != nil {
			fmt.Println("failed")
			fmt.Println("  ", err)
			if !confirm(in, "Try again?", true) {
				return errors.New("setup canceled")
			}
			fmt.Println()
			continue
		}
		fmt.Println("ok —", server)

		cfg.Password = ""
		if err := cfg.StorePassword(password); err != nil {
			fmt.Printf("Could not save the password to the OS keyring: %v\n", err)
			if confirm(in, "Store it in the config file in plain text instead?", false) {
				cfg.Password = password
			} else {
				fmt.Println("  OK — set the PULSETUI_PASSWORD environment variable before starting pulse.")
			}
		} else {
			fmt.Println("Password saved to the OS keyring.")
		}

		cfg.Source = config.SourceNavidrome
		if err := cfg.Save(path); err != nil {
			return fmt.Errorf("saving config: %w", err)
		}
		fmt.Println("Config saved to", path)
		fmt.Println()
		return nil
	}
}

func prompt(in *bufio.Reader, label, def string) string {
	if def != "" {
		fmt.Printf("%s [%s]: ", label, def)
	} else {
		fmt.Printf("%s: ", label)
	}
	line, _ := in.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

func promptPassword(in *bufio.Reader, label string) (string, error) {
	fmt.Printf("%s: ", label)
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		line, err := in.ReadString('\n')
		if err != nil && line == "" {
			return "", err
		}
		return strings.TrimSpace(line), nil
	}
	pw, err := term.ReadPassword(fd)
	fmt.Println()
	if err != nil {
		return "", err
	}
	return string(pw), nil
}

func confirm(in *bufio.Reader, question string, def bool) bool {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	fmt.Printf("%s [%s]: ", question, hint)
	line, _ := in.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	case "n", "no":
		return false
	}
	return def
}
