package parakeetinstall

import (
	"errors"
	"fmt"
)

const (
	runtimeVersion = "0.1.0"
	runtimeBaseURL = "https://github.com/NVIDIA/NeMo-Speech.cpp/releases/download/v0.1.0/"
	modelURL       = "https://huggingface.co/nvidia/parakeet-tdt-0.6b-v3/resolve/541d1f99c6b0c3cd0b11a95167540bb8edefd82b/parakeet-tdt-0.6b-v3.q8_0.gguf"
	modelSize      = 713975456
	modelSHA256    = "e3880d0aaaaf2c308ea2c35016b2b895c423eb3fda924c1b463d1c19b7f4d32e"
)

var errUnsupportedPlatform = errors.New("Parakeet installation is unsupported on this platform")

type artifact struct {
	URL    string
	Size   int64
	SHA256 string
	Root   string
}

type plan struct {
	Identity string
	Runtime  artifact
	Model    artifact
}

type runtimeArtifact struct {
	name   string
	size   int64
	digest string
}

func officialPlan(platform, arch string) (plan, error) {
	if platform == "wsl" {
		platform = "linux"
	}
	artifacts := map[string]runtimeArtifact{
		"linux/arm64": {
			name: "nemo-speech-0.1.0-linux-aarch64-cpu.tar.gz", size: 4328117,
			digest: "0e4112255d566de7bdd142f239e984995c4447103ba8feb41f2bb5c559d561d3",
		},
		"linux/amd64": {
			name: "nemo-speech-0.1.0-linux-x86_64-cpu.tar.gz", size: 4583913,
			digest: "0f74131d631ad2c694cf0ec53490866bb6461147959589a69fb6fc231944065b",
		},
		"macos/arm64": {
			name: "nemo-speech-0.1.0-macos-aarch64-metal.tar.gz", size: 3465028,
			digest: "f1dff4f9dd9c96214f8cb78b982812459132df8a4ad1a42409fd94de4a366244",
		},
		"macos/amd64": {
			name: "nemo-speech-0.1.0-macos-x86_64-cpu.tar.gz", size: 3618245,
			digest: "042a4612e07460fab6a39b5d862aa1e39d0ac3eaedfdb979f3f5fc12de510c20",
		},
	}
	selected, ok := artifacts[platform+"/"+arch]
	if !ok {
		return plan{}, fmt.Errorf("%w: %s/%s", errUnsupportedPlatform, platform, arch)
	}
	return plan{
		Identity: "nemo-speech " + runtimeVersion + " + nvidia/parakeet-tdt-0.6b-v3@541d1f99c6b0c3cd0b11a95167540bb8edefd82b",
		Runtime: artifact{
			URL: runtimeBaseURL + selected.name, Size: selected.size, SHA256: selected.digest,
			Root: selected.name[:len(selected.name)-len(".tar.gz")],
		},
		Model: artifact{URL: modelURL, Size: modelSize, SHA256: modelSHA256},
	}, nil
}
