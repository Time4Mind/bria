package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/inbound"
	"github.com/Time4Mind/bria/internal/runtimehost"
	"github.com/Time4Mind/bria/internal/telegrambot"
)

const maxInboundTelegramBytes = telegrambot.MaxTelegramFileBytes

type runtimeInputResolver struct {
	downloader      inbound.Downloader
	transcribers    map[string]inbound.Transcriber
	defaultVoice    string
	maxDownloadSize int64
	temporary       string
}

type telegramInboundDownloader struct{ client *telegrambot.Client }

func (d telegramInboundDownloader) Download(
	ctx context.Context,
	fileID string,
	destination io.Writer,
	maxBytes int64,
) (int64, error) {
	written, err := d.client.Download(ctx, fileID, destination, maxBytes)
	if errors.Is(err, telegrambot.ErrTelegramFileTooLarge) {
		return written, inbound.ErrMediaTooLarge
	}
	return written, err
}

func (r runtimeInputResolver) ResolveInput(
	ctx context.Context,
	workdir string,
	payload runtimehost.InputPayload,
) (string, error) {
	var transcriber inbound.Transcriber
	if payload.Kind == runtimehost.InputVoice {
		voiceBackend := strings.ToLower(strings.TrimSpace(payload.VoiceBackend))
		if voiceBackend == "" || voiceBackend == "auto" {
			voiceBackend = r.defaultVoice
		}
		if voiceBackend == "off" {
			return "", errors.New("voice recognition is disabled")
		}
		transcriber = r.transcribers[voiceBackend]
		if transcriber == nil {
			return "", errors.New("selected voice recognition backend is unavailable on this node")
		}
	}
	processor, err := inbound.NewProcessor(inbound.ProcessorConfig{
		Downloader: r.downloader, Transcriber: transcriber,
		MaxDownloadBytes: r.maxDownloadSize, TempDir: r.temporary,
	})
	if err != nil {
		return "", err
	}
	result, err := processor.Process(ctx, workdir, inbound.Input{
		Kind: inbound.Kind(payload.Kind), Text: payload.Caption,
		FileID: payload.File.ID, UniqueID: payload.File.UniqueID,
		FileName: payload.File.Name, MIMEType: payload.File.MIMEType, Size: payload.File.Size,
		Language: payload.VoiceLanguage,
	})
	if err != nil {
		return "", err
	}
	if result.Kind == inbound.KindVoice {
		return joinInputParts(payload.Origin, payload.Caption, result.Text), nil
	}
	path := filepath.ToSlash(result.RelativePath)
	return joinInputParts(payload.Origin, result.Caption, path), nil
}

func joinInputParts(parts ...string) string {
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return strings.Join(result, "\n\n")
}

func configureInboundResolver(
	executor *runtimehost.LocalExecutor,
	nodeConfig config.Config,
) error {
	token, enabled, err := loadOptionalTelegramToken(nodeConfig.TelegramTokenFile)
	if err != nil || !enabled {
		return err
	}
	client, err := telegrambot.NewClient(telegrambot.ClientConfig{Token: token})
	if err != nil {
		return err
	}
	temporary := filepath.Join(nodeConfig.DataDir, "tmp")
	if err := os.MkdirAll(temporary, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(temporary, 0o700); err != nil {
		return err
	}
	if executor == nil {
		return errors.New("runtime executor is required")
	}
	transcribers := make(map[string]inbound.Transcriber, 2)
	whisper, err := configuredWhisperTranscriber(nodeConfig, temporary)
	if err != nil {
		return err
	}
	transcribers[config.SpeechEngineWhisper] = whisper
	if runtime.GOOS == "darwin" {
		apple, appleErr := configuredAppleTranscriber(nodeConfig, temporary)
		if appleErr != nil {
			return appleErr
		}
		transcribers[config.SpeechEngineApple] = apple
	}
	defaultVoice := nodeConfig.EffectiveSpeechEngine()
	if runtime.GOOS == "darwin" {
		defaultVoice = config.SpeechEngineApple
	}
	executor.SetInputResolver(runtimeInputResolver{
		downloader: telegramInboundDownloader{client: client}, transcribers: transcribers,
		defaultVoice: defaultVoice, maxDownloadSize: maxInboundTelegramBytes,
		temporary: temporary,
	})
	return nil
}

func configuredWhisperTranscriber(
	nodeConfig config.Config,
	temporary string,
) (inbound.Transcriber, error) {
	command := nodeConfig.WhisperCommand
	if _, err := exec.LookPath(command); err != nil {
		candidate := filepath.Join(nodeConfig.DataDir, "speech", "bin", "whisper-cli")
		if runtime.GOOS == "windows" {
			candidate += ".exe"
		}
		command = candidate
	}
	return inbound.NewWhisperTranscriber(inbound.WhisperConfig{
		FFmpegBinary: nodeConfig.FFmpegCommand, WhisperBinary: command,
		ModelPath: nodeConfig.WhisperModelPath, Language: nodeConfig.WhisperLanguage,
		Threads: nodeConfig.WhisperThreads, TempDir: temporary,
	})
}

func configuredAppleTranscriber(
	nodeConfig config.Config,
	temporary string,
) (inbound.Transcriber, error) {
	command := nodeConfig.AppleSpeechCommand
	if _, err := exec.LookPath(command); err != nil {
		command = filepath.Join(nodeConfig.DataDir, "speech", "bin", "bria-apple-speech")
	}
	return inbound.NewAppleSpeechTranscriber(inbound.AppleSpeechConfig{
		FFmpegBinary: nodeConfig.FFmpegCommand,
		SpeechBinary: command,
		Language:     nodeConfig.WhisperLanguage, TempDir: temporary,
	})
}

func configuredTranscriber(
	nodeConfig config.Config,
	temporary string,
	operatingSystem string,
) (inbound.Transcriber, error) {
	switch nodeConfig.EffectiveSpeechEngine() {
	case config.SpeechEngineWhisper:
		return configuredWhisperTranscriber(nodeConfig, temporary)
	case config.SpeechEngineApple:
		if operatingSystem != "darwin" {
			return nil, errors.New("the apple speech engine is available only on macOS")
		}
		return configuredAppleTranscriber(nodeConfig, temporary)
	default:
		return nil, errors.New("unsupported speech engine")
	}
}
