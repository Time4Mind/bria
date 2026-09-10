package nativeadapter

import "bria/internal/nativestartupdiagnostic"

// StartupStage remains an alias for the adapter's public startup diagnostics API.
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
