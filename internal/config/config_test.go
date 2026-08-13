package config_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Time4Mind/bria/internal/config"
)

func TestLoadRejectsUnknownFieldsAndRelativeSecretPaths(t *testing.T) {
	directory := t.TempDir()
	unknown := filepath.Join(directory, "unknown.json")
	if err := os.WriteFile(unknown, []byte(`{"cluster_id":"c","node_id":"n","node_name":"N","unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(unknown); err == nil {
		t.Fatal("unknown config field accepted")
	}

	invalid := config.Config{
		ClusterID: "c", NodeID: "n", NodeName: "N", DataDir: directory,
		RaftBind: "127.0.0.1:1", RaftAdvertise: "127.0.0.1:1",
		CACertificate: "/ca", CAPrivateKey: "/ca-key", NodeCertificate: "/cert", NodePrivateKey: "relative",
		TelegramTokenFile: "/token", CallbackKeyFile: "/callback-key", TmuxSession: "bria",
		ClaudeCommand: "claude", CodexCommand: "codex",
	}
	if err := invalid.Validate(); err == nil {
		t.Fatal("relative private key path accepted")
	}
}

func TestValidConfigDoesNotRequireSystemdOrCPUSelection(t *testing.T) {
	directory := t.TempDir()
	valid := config.Config{
		ClusterID: "cluster", NodeID: "node", NodeName: "Node", DataDir: directory,
		RaftBind: "127.0.0.1:7946", RaftAdvertise: "node.internal:7946",
		CACertificate:     filepath.Join(directory, "ca"),
		NodeCertificate:   filepath.Join(directory, "cert"),
		NodePrivateKey:    filepath.Join(directory, "key"),
		TelegramTokenFile: filepath.Join(directory, "token"), CallbackKeyFile: filepath.Join(directory, "callback-key"), TmuxSession: "bria",
		ClaudeCommand: "claude", CodexCommand: "codex",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestDefaultConfigEnablesNodeLocalWhisper(t *testing.T) {
	configured, err := config.Default()
	if err != nil {
		t.Fatal(err)
	}
	if configured.FFmpegCommand != "ffmpeg" || configured.WhisperCommand != "whisper-cli" ||
		configured.WhisperLanguage != "auto" || configured.WhisperThreads < 1 ||
		!filepath.IsAbs(configured.WhisperModelPath) ||
		configured.EffectiveSpeechEngine() != config.SpeechEngineWhisper ||
		configured.AppleSpeechCommand == "" {
		t.Fatalf("voice defaults=%#v", configured)
	}
}

func TestAppleSpeechEngineRequiresHelperCommand(t *testing.T) {
	configured := validConfig(t)
	configured.SpeechEngine = config.SpeechEngineApple
	configured.AppleSpeechCommand = ""
	if err := configured.Validate(); err == nil {
		t.Fatal("Apple Speech without a helper command was accepted")
	}
	configured.AppleSpeechCommand = "bria-apple-speech"
	if err := configured.Validate(); err != nil {
		t.Fatalf("valid Apple Speech configuration rejected: %v", err)
	}
}

func TestWhisperConfigurationRejectsUnsafeValues(t *testing.T) {
	configured := validConfig(t)
	configured.WhisperModelPath = "relative-model.bin"
	configured.FFmpegCommand = "ffmpeg"
	configured.WhisperCommand = "whisper-cli"
	configured.WhisperLanguage = "auto"
	configured.WhisperThreads = 4
	if err := configured.Validate(); err == nil {
		t.Fatal("relative whisper model path accepted")
	}
}

func TestBootstrapRequiresCAPrivateKeyAndJoinNodeRejectsIt(t *testing.T) {
	bootstrap := validConfig(t)
	bootstrap.Bootstrap = true
	if err := bootstrap.Validate(); err == nil {
		t.Fatal("bootstrap config without CA private key accepted")
	}
	bootstrap.CAPrivateKey = filepath.Join(t.TempDir(), "ca.key")
	if err := bootstrap.Validate(); err != nil {
		t.Fatalf("bootstrap config with CA private key rejected: %v", err)
	}

	join := validConfig(t)
	join.CAPrivateKey = filepath.Join(t.TempDir(), "ca.key")
	if err := join.Validate(); err == nil {
		t.Fatal("join config with CA private key accepted")
	}
}

func TestRaftPeersRequireUniqueRoutableAddressesAndExactSelfMapping(t *testing.T) {
	base := validConfig(t)
	base.RaftPeers = []config.RaftPeer{
		{NodeID: "node", NodeName: "Local", Address: base.RaftAdvertise},
		{NodeID: "peer", NodeName: "Peer", Address: "peer.internal:7946"},
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid peer set rejected: %v", err)
	}

	tests := []struct {
		name  string
		peers []config.RaftPeer
	}{
		{"missing self", []config.RaftPeer{{NodeID: "peer", NodeName: "Peer", Address: "peer.internal:7946"}}},
		{"wrong self address", []config.RaftPeer{{NodeID: "node", NodeName: "Local", Address: "other.internal:7946"}}},
		{"duplicate id", []config.RaftPeer{{NodeID: "node", NodeName: "A", Address: base.RaftAdvertise}, {NodeID: "node", NodeName: "B", Address: "peer.internal:7946"}}},
		{"duplicate address", []config.RaftPeer{{NodeID: "node", NodeName: "A", Address: base.RaftAdvertise}, {NodeID: "peer", NodeName: "B", Address: base.RaftAdvertise}}},
		{"wildcard address", []config.RaftPeer{{NodeID: "node", NodeName: "A", Address: base.RaftAdvertise}, {NodeID: "peer", NodeName: "B", Address: "0.0.0.0:7946"}}},
		{"star wildcard", []config.RaftPeer{{NodeID: "node", NodeName: "A", Address: base.RaftAdvertise}, {NodeID: "peer", NodeName: "B", Address: "*:7946"}}},
		{"invalid port", []config.RaftPeer{{NodeID: "node", NodeName: "A", Address: base.RaftAdvertise}, {NodeID: "peer", NodeName: "B", Address: "peer.internal:raft"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := base
			candidate.RaftPeers = test.peers
			if err := candidate.Validate(); err == nil {
				t.Fatal("invalid peer set accepted")
			}
		})
	}
}

func TestRaftAdvertiseRejectsWildcardButBindAllowsIt(t *testing.T) {
	candidate := validConfig(t)
	candidate.RaftBind = "[::]:7946"
	if err := candidate.Validate(); err != nil {
		t.Fatalf("wildcard bind rejected: %v", err)
	}
	candidate.RaftAdvertise = "[::]:7946"
	if err := candidate.Validate(); err == nil {
		t.Fatal("wildcard advertised address accepted")
	}
}

func TestControlAddressesDefaultToAdjacentRaftPort(t *testing.T) {
	candidate := validConfig(t)
	bind, err := candidate.ControlBindAddress()
	if err != nil {
		t.Fatal(err)
	}
	advertise, err := candidate.ControlAdvertiseAddress()
	if err != nil {
		t.Fatal(err)
	}
	if bind != "127.0.0.1:7947" || advertise != "node.internal:7947" {
		t.Fatalf("control addresses=%q/%q", bind, advertise)
	}
	enrollmentBind, err := candidate.EnrollmentBindAddress()
	if err != nil {
		t.Fatal(err)
	}
	enrollmentAdvertise, err := candidate.EnrollmentAdvertiseAddress()
	if err != nil {
		t.Fatal(err)
	}
	if enrollmentBind != "127.0.0.1:7948" || enrollmentAdvertise != "node.internal:7948" {
		t.Fatalf("enrollment addresses=%q/%q", enrollmentBind, enrollmentAdvertise)
	}
	peer := config.RaftPeer{Address: "peer.internal:17946"}
	address, err := peer.EffectiveControlAddress()
	if err != nil {
		t.Fatal(err)
	}
	if address != "peer.internal:17947" {
		t.Fatalf("peer control address=%q", address)
	}
}

func TestExplicitControlAddressesAllowNonAdjacentPorts(t *testing.T) {
	candidate := validConfig(t)
	candidate.ControlBind = "0.0.0.0:9443"
	candidate.ControlAdvertise = "node.internal:443"
	candidate.RaftPeers = []config.RaftPeer{{
		NodeID: "node", NodeName: "Node", Address: candidate.RaftAdvertise,
		ControlAddress: candidate.ControlAdvertise,
	}}
	if err := candidate.Validate(); err != nil {
		t.Fatalf("explicit control addresses rejected: %v", err)
	}
}

func TestPeerDialOverridesAreLocalAndValidated(t *testing.T) {
	candidate := validConfig(t)
	peer := config.RaftPeer{
		NodeID: "node", NodeName: "Node", Address: candidate.RaftAdvertise,
		ControlAddress: "node.internal:7947", DialAddress: "127.0.0.1:19046",
		ControlDialAddress: "127.0.0.1:19047",
	}
	candidate.RaftPeers = []config.RaftPeer{peer}
	if err := candidate.Validate(); err != nil {
		t.Fatalf("valid dial overrides rejected: %v", err)
	}
	if peer.EffectiveDialAddress() != "127.0.0.1:19046" {
		t.Fatalf("raft dial address=%q", peer.EffectiveDialAddress())
	}
	control, err := peer.EffectiveControlDialAddress()
	if err != nil || control != "127.0.0.1:19047" {
		t.Fatalf("control dial address=%q/%v", control, err)
	}

	for _, field := range []string{"raft", "control"} {
		broken := candidate
		broken.RaftPeers = append([]config.RaftPeer(nil), candidate.RaftPeers...)
		if field == "raft" {
			broken.RaftPeers[0].DialAddress = "0.0.0.0:19046"
		} else {
			broken.RaftPeers[0].ControlDialAddress = "not-an-address"
		}
		if err := broken.Validate(); err == nil {
			t.Fatalf("invalid %s dial override accepted", field)
		}
	}
}

func TestLoadParsesPeerDialOverrides(t *testing.T) {
	candidate := validConfig(t)
	candidate.RaftPeers = []config.RaftPeer{{
		NodeID: "node", NodeName: "Node", Address: candidate.RaftAdvertise,
		DialAddress: "127.0.0.1:19046", ControlDialAddress: "127.0.0.1:19047",
	}}
	encoded, err := json.Marshal(candidate)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.RaftPeers[0]; got.DialAddress != "127.0.0.1:19046" ||
		got.ControlDialAddress != "127.0.0.1:19047" {
		t.Fatalf("loaded overrides=%#v", got)
	}
}

func TestTelegramProxyConfigurationIsTyped(t *testing.T) {
	candidate := validConfig(t)
	candidate.HTTPProxy = "http://127.0.0.1:1081"
	if err := candidate.Validate(); err != nil || candidate.TelegramProxyURL() != candidate.HTTPProxy {
		t.Fatalf("HTTP proxy rejected: %v", err)
	}
	candidate.HTTPProxy = ""
	candidate.SOCKS5Proxy = "socks5://127.0.0.1:1080"
	if err := candidate.Validate(); err != nil || candidate.TelegramProxyURL() != candidate.SOCKS5Proxy {
		t.Fatalf("SOCKS proxy rejected: %v", err)
	}
	candidate.SOCKS5Proxy = ""
	candidate.HTTPProxy = "file:///tmp/socket"
	if err := candidate.Validate(); err == nil {
		t.Fatal("unsupported proxy URL accepted")
	}
}

func TestControlAddressDerivationRejectsExhaustedPort(t *testing.T) {
	candidate := validConfig(t)
	candidate.RaftBind = "127.0.0.1:65535"
	if err := candidate.Validate(); err == nil {
		t.Fatal("control address derived past port range")
	}
}

func validConfig(t *testing.T) config.Config {
	t.Helper()
	directory := t.TempDir()
	return config.Config{
		ClusterID: "cluster", NodeID: "node", NodeName: "Node", DataDir: directory,
		RaftBind: "127.0.0.1:7946", RaftAdvertise: "node.internal:7946",
		CACertificate: filepath.Join(directory, "ca"), NodeCertificate: filepath.Join(directory, "cert"),
		NodePrivateKey: filepath.Join(directory, "key"), TelegramTokenFile: filepath.Join(directory, "token"),
		CallbackKeyFile: filepath.Join(directory, "callback-key"), TmuxSession: "bria",
		ClaudeCommand: "claude", CodexCommand: "codex",
	}
}
