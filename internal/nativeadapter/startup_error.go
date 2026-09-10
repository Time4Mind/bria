package nativeadapter

import (
	"errors"

	"bria/internal/nativestartupdiagnostic"
	"bria/internal/nativeterminal"
)

type terminalUnavailableStartupError struct{ error }

func (terminalUnavailableStartupError) NativeStartupFailureClass() string {
	return "terminal_unavailable"
}
func (e terminalUnavailableStartupError) Unwrap() error { return e.error }

type StartupStage = nativestartupdiagnostic.Stage

const (
	StartupStageUnknown               = nativestartupdiagnostic.StageUnknown
	StartupStageOpenTerminal          = nativestartupdiagnostic.StageOpenTerminal
	StartupStageNativeReadinessStatus = nativestartupdiagnostic.StageNativeReadinessStatus
	StartupStageBindingPersistence    = nativestartupdiagnostic.StageBindingPersistence
	StartupStageReceiptBaseline       = nativestartupdiagnostic.StageReceiptBaseline
	StartupStageProtocolReadyEmission = nativestartupdiagnostic.StageProtocolReadyEmission
)

func atStartupStage(stage StartupStage, err error) error {
	if errors.Is(err, nativeterminal.ErrTerminalUnavailable) {
		err = terminalUnavailableStartupError{err}
	}
	return nativestartupdiagnostic.AtStage(stage, err)
}

func StartupFailureStage(err error) StartupStage {
	return nativestartupdiagnostic.FailureStage(err)
}

func StartupFailureMarker(err error) string {
	return nativestartupdiagnostic.FailureMarker(err)
}

func StartupFailureClass(err error) string {
	return nativestartupdiagnostic.FailureClass(err)
}
