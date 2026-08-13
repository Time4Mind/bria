package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Time4Mind/bria/internal/callbacktoken"
	"github.com/Time4Mind/bria/internal/config"
)

func TestInitClusterCreatesPrivateRuntimeIdentityAndCallbackKey(t *testing.T) {
	dataDirectory := filepath.Join(t.TempDir(), "data")
	configPath := filepath.Join(dataDirectory, "config.json")
	err := initCluster([]string{
		"init",
		"--cluster-id", "test-cluster",
		"--node-id", "test-node",
		"--node-name", "Test node",
		"--data-dir", dataDirectory,
		"--config", configPath,
		"--raft-bind", "127.0.0.1:17946",
		"--raft-advertise", "127.0.0.1:17946",
	})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	codec, err := callbacktoken.LoadFile(loaded.CallbackKeyFile)
	if err != nil || codec == nil {
		t.Fatalf("load callback key=(%v, %v)", codec, err)
	}
	paths := []string{
		configPath, loaded.CACertificate, loaded.CAPrivateKey,
		loaded.NodeCertificate, loaded.NodePrivateKey, loaded.CallbackKeyFile,
	}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatalf("%s permissions=%#o", path, info.Mode().Perm())
		}
	}
	key, err := os.ReadFile(loaded.CallbackKeyFile)
	if err != nil || len(key) != callbacktoken.KeyBytes {
		t.Fatalf("callback key size=%d error=%v", len(key), err)
	}
	if _, err := os.Stat(loaded.TelegramTokenFile); !os.IsNotExist(err) {
		t.Fatalf("Telegram token should remain operator-provided: %v", err)
	}
}
