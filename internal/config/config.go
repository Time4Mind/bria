// Package config loads node-local, non-replicated Bria configuration.
package config

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	OfficialUpdateManifestURL = "https://github.com/Time4Mind/bria/releases/latest/download/release-manifest.json"
	OfficialUpdatePublicKey   = "yuK6610b5dA8jVTCUsG8GslvNtvsL7MEGY9/Yab5Sb4="
)

type RaftPeer struct {
	NodeID             string `json:"node_id"`
	NodeName           string `json:"node_name"`
	Address            string `json:"address"`
	ControlAddress     string `json:"control_address,omitempty"`
	DialAddress        string `json:"dial_address,omitempty"`
	ControlDialAddress string `json:"control_dial_address,omitempty"`
}

type Config struct {
	ClusterID           string       `json:"cluster_id"`
	NodeID              string       `json:"node_id"`
	NodeName            string       `json:"node_name"`
	BootstrapOwnerID    int64        `json:"bootstrap_owner_user_id,omitempty"`
	DataDir             string       `json:"data_dir"`
	RaftBind            string       `json:"raft_bind"`
	RaftAdvertise       string       `json:"raft_advertise"`
	ControlBind         string       `json:"control_bind,omitempty"`
	ControlAdvertise    string       `json:"control_advertise,omitempty"`
	EnrollmentBind      string       `json:"enrollment_bind,omitempty"`
	EnrollmentAdvertise string       `json:"enrollment_advertise,omitempty"`
	EnrollmentIssuerID  string       `json:"enrollment_issuer_node_id,omitempty"`
	RaftPeers           []RaftPeer   `json:"raft_peers,omitempty"`
	Bootstrap           bool         `json:"bootstrap"`
	CACertificate       string       `json:"ca_certificate"`
	CAPrivateKey        string       `json:"ca_private_key"`
	NodeCertificate     string       `json:"node_certificate"`
	NodePrivateKey      string       `json:"node_private_key"`
	TelegramTokenFile   string       `json:"telegram_token_file"`
	CallbackKeyFile     string       `json:"callback_key_file"`
	TmuxSession         string       `json:"tmux_session"`
	ClaudeCommand       string       `json:"claude_command"`
	ClaudeFlags         []string     `json:"claude_flags,omitempty"`
	ClaudeNamingModel   string       `json:"claude_naming_model,omitempty"`
	CodexCommand        string       `json:"codex_command"`
	CodexFlags          []string     `json:"codex_flags,omitempty"`
	CodexNamingModel    string       `json:"codex_naming_model,omitempty"`
	SpeechEngine        string       `json:"speech_engine,omitempty"`
	FFmpegCommand       string       `json:"ffmpeg_command,omitempty"`
	WhisperCommand      string       `json:"whisper_command,omitempty"`
	WhisperModelPath    string       `json:"whisper_model_path,omitempty"`
	WhisperLanguage     string       `json:"whisper_language,omitempty"`
	WhisperThreads      int          `json:"whisper_threads,omitempty"`
	AppleSpeechCommand  string       `json:"apple_speech_command,omitempty"`
	HTTPProxy           string       `json:"http_proxy,omitempty"`
	SOCKS5Proxy         string       `json:"socks5_proxy,omitempty"`
	UpdateManifestURL   string       `json:"update_manifest_url,omitempty"`
	UpdatePublicKey     string       `json:"update_public_key,omitempty"`
	UpdateInstallRoot   string       `json:"update_install_root,omitempty"`
	Runner              RunnerConfig `json:"runner,omitempty"`
}

func Default() (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, fmt.Errorf("resolve home directory: %w", err)
	}
	dataDir := filepath.Join(home, ".bria")
	return Config{
		DataDir:            dataDir,
		RaftBind:           "127.0.0.1:7946",
		RaftAdvertise:      "127.0.0.1:7946",
		CACertificate:      filepath.Join(dataDir, "pki", "ca.crt"),
		NodeCertificate:    filepath.Join(dataDir, "pki", "node.crt"),
		NodePrivateKey:     filepath.Join(dataDir, "pki", "node.key"),
		TelegramTokenFile:  filepath.Join(dataDir, "secrets", "telegram.token"),
		CallbackKeyFile:    filepath.Join(dataDir, "secrets", "callback.key"),
		TmuxSession:        "bria",
		ClaudeCommand:      "claude",
		ClaudeNamingModel:  "haiku",
		CodexCommand:       "codex",
		CodexNamingModel:   "gpt-5.6-luna",
		SpeechEngine:       SpeechEngineWhisper,
		FFmpegCommand:      "ffmpeg",
		WhisperCommand:     "whisper-cli",
		WhisperModelPath:   filepath.Join(dataDir, "models", "ggml-medium-q8_0.bin"),
		WhisperLanguage:    "auto",
		WhisperThreads:     4,
		AppleSpeechCommand: "bria-apple-speech",
		UpdateManifestURL:  OfficialUpdateManifestURL,
		UpdatePublicKey:    OfficialUpdatePublicKey,
	}, nil
}

func Load(path string) (Config, error) {
	config, err := Default()
	if err != nil {
		return Config{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open config: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Config{}, errors.New("config contains trailing JSON values")
		}
		return Config{}, fmt.Errorf("decode config trailer: %w", err)
	}
	config.expandPaths()
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (c Config) Validate() error {
	if c.BootstrapOwnerID < 0 {
		return errors.New("bootstrap_owner_user_id must not be negative")
	}
	for label, value := range map[string]string{
		"cluster_id": c.ClusterID,
		"node_id":    c.NodeID,
		"node_name":  c.NodeName,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", label)
		}
	}
	if !filepath.IsAbs(c.DataDir) {
		return errors.New("data_dir must be absolute")
	}
	if err := validateHostPort("raft_bind", c.RaftBind, true); err != nil {
		return err
	}
	if err := validateHostPort("raft_advertise", c.RaftAdvertise, false); err != nil {
		return err
	}
	controlBind, err := c.ControlBindAddress()
	if err != nil {
		return fmt.Errorf("control_bind: %w", err)
	}
	if err := validateHostPort("control_bind", controlBind, true); err != nil {
		return err
	}
	controlAdvertise, err := c.ControlAdvertiseAddress()
	if err != nil {
		return fmt.Errorf("control_advertise: %w", err)
	}
	if err := validateHostPort("control_advertise", controlAdvertise, false); err != nil {
		return err
	}
	enrollmentAdvertise, err := c.EnrollmentAdvertiseAddress()
	if err != nil {
		return fmt.Errorf("enrollment_advertise: %w", err)
	}
	if err := validateHostPort("enrollment_advertise", enrollmentAdvertise, false); err != nil {
		return err
	}
	if c.Bootstrap {
		enrollmentBind, bindErr := c.EnrollmentBindAddress()
		if bindErr != nil {
			return fmt.Errorf("enrollment_bind: %w", bindErr)
		}
		if err := validateHostPort("enrollment_bind", enrollmentBind, true); err != nil {
			return err
		}
	}
	if err := c.validateRaftPeers(); err != nil {
		return err
	}
	for label, path := range map[string]string{
		"ca_certificate":      c.CACertificate,
		"node_certificate":    c.NodeCertificate,
		"node_private_key":    c.NodePrivateKey,
		"telegram_token_file": c.TelegramTokenFile,
		"callback_key_file":   c.CallbackKeyFile,
	} {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("%s must be absolute", label)
		}
	}
	if c.Bootstrap {
		if c.CAPrivateKey == "" || !filepath.IsAbs(c.CAPrivateKey) {
			return errors.New("ca_private_key must be absolute for a bootstrap node")
		}
	} else if c.CAPrivateKey != "" {
		return errors.New("ca_private_key is only allowed on a bootstrap node")
	}
	if strings.TrimSpace(c.TmuxSession) == "" {
		return errors.New("tmux_session is required")
	}
	if strings.TrimSpace(c.ClaudeCommand) == "" || strings.TrimSpace(c.CodexCommand) == "" {
		return errors.New("claude_command and codex_command are required")
	}
	switch c.EffectiveSpeechEngine() {
	case SpeechEngineWhisper:
	case SpeechEngineApple:
		if strings.TrimSpace(c.AppleSpeechCommand) == "" {
			return errors.New("apple_speech_command is required for the apple speech engine")
		}
	default:
		return errors.New("speech_engine must be whisper or apple")
	}
	if c.WhisperModelPath != "" {
		if !filepath.IsAbs(c.WhisperModelPath) {
			return errors.New("whisper_model_path must be absolute")
		}
		if strings.TrimSpace(c.FFmpegCommand) == "" || strings.TrimSpace(c.WhisperCommand) == "" ||
			strings.TrimSpace(c.WhisperLanguage) == "" || c.WhisperThreads < 1 || c.WhisperThreads > 256 {
			return errors.New("whisper configuration is invalid")
		}
	}
	if c.HTTPProxy != "" && c.SOCKS5Proxy != "" {
		return errors.New("configure either http_proxy or socks5_proxy, not both")
	}
	if err := validateProxyURL("http_proxy", c.HTTPProxy, "http", "https"); err != nil {
		return err
	}
	if err := validateProxyURL("socks5_proxy", c.SOCKS5Proxy, "socks5", "socks5h"); err != nil {
		return err
	}
	if (c.UpdateManifestURL == "") != (c.UpdatePublicKey == "") {
		return errors.New("update_manifest_url and update_public_key must be configured together")
	}
	if c.UpdateManifestURL != "" {
		if err := validateUpdateManifestURL(c.UpdateManifestURL); err != nil {
			return err
		}
		key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(c.UpdatePublicKey))
		if err != nil || len(key) != 32 {
			return errors.New("update_public_key must be a base64 Ed25519 public key")
		}
	}
	if c.UpdateInstallRoot != "" && !filepath.IsAbs(c.UpdateInstallRoot) {
		return errors.New("update_install_root must be absolute")
	}
	if err := c.validateRunner(); err != nil {
		return err
	}
	return nil
}

const (
	SpeechEngineWhisper = "whisper"
	SpeechEngineApple   = "apple"
)

func (c Config) EffectiveSpeechEngine() string {
	engine := strings.ToLower(strings.TrimSpace(c.SpeechEngine))
	if engine == "" {
		return SpeechEngineWhisper
	}
	return engine
}

func (c Config) EffectiveClaudeFlags() []string {
	if len(c.ClaudeFlags) == 0 {
		return []string{"--dangerously-skip-permissions"}
	}
	return append([]string(nil), c.ClaudeFlags...)
}

func (c Config) EffectiveCodexFlags() []string {
	if len(c.CodexFlags) == 0 {
		return []string{
			"--dangerously-bypass-approvals-and-sandbox",
			"--dangerously-bypass-hook-trust",
			"--enable", "hooks", "--no-alt-screen",
		}
	}
	return append([]string(nil), c.CodexFlags...)
}

func (c Config) TelegramProxyURL() string {
	if c.HTTPProxy != "" {
		return c.HTTPProxy
	}
	return c.SOCKS5Proxy
}

func (c *Config) expandPaths() {
	c.DataDir = filepath.Clean(c.DataDir)
	c.CACertificate = filepath.Clean(c.CACertificate)
	if c.CAPrivateKey != "" {
		c.CAPrivateKey = filepath.Clean(c.CAPrivateKey)
	}
	c.NodeCertificate = filepath.Clean(c.NodeCertificate)
	c.NodePrivateKey = filepath.Clean(c.NodePrivateKey)
	c.TelegramTokenFile = filepath.Clean(c.TelegramTokenFile)
	c.CallbackKeyFile = filepath.Clean(c.CallbackKeyFile)
	if c.WhisperModelPath != "" {
		c.WhisperModelPath = filepath.Clean(c.WhisperModelPath)
	}
	if c.UpdateInstallRoot != "" {
		c.UpdateInstallRoot = filepath.Clean(c.UpdateInstallRoot)
	}
	c.expandRunnerPaths()
}
