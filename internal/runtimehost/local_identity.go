package runtimehost

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

func validateBinding(binding RuntimeBinding, localNodeID string) error {
	if err := validateBindingIdentity(binding, localNodeID); err != nil {
		return err
	}
	if strings.TrimSpace(binding.TmuxTarget) == "" {
		return errors.New("tmux target is required")
	}
	return nil
}

func validatePreparedBinding(binding RuntimeBinding, localNodeID string) error {
	if err := validateBindingIdentity(binding, localNodeID); err != nil {
		return err
	}
	if strings.TrimSpace(binding.TmuxTarget) != "" {
		return errors.New("prepared binding must not have a tmux target")
	}
	return nil
}

func validateBindingIdentity(binding RuntimeBinding, localNodeID string) error {
	if strings.TrimSpace(binding.NodeID) == "" || binding.NodeID != localNodeID {
		return errors.New("binding must belong to the local node")
	}
	if strings.TrimSpace(binding.SessionID) == "" || binding.Generation == 0 {
		return errors.New("session id and generation are required")
	}
	if strings.TrimSpace(binding.Backend) == "" {
		return errors.New("backend is required")
	}
	if binding.Workdir != "" && !filepath.IsAbs(binding.Workdir) {
		return errors.New("binding workdir must be absolute")
	}
	return nil
}

func runtimeKey(nodeID, sessionID string) string {
	return nodeID + "\x00" + sessionID
}

func requestFingerprint(request Request) string {
	archivePayload, _ := json.Marshal(request.Archive)
	inputPayload, _ := json.Marshal(request.Input)
	value := fmt.Sprintf(
		"%d\x00%s\x00%s\x00%d\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s",
		request.ActorID, request.NodeID, request.SessionID, request.ExpectedGeneration,
		request.Action, strings.ToLower(request.Backend), request.Text, request.ArchiveCommitID,
		archivePayload, inputPayload, request.Key, request.ExpectedPromptHash,
	)
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
