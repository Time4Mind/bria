package main

import (
	"net"

	"github.com/Time4Mind/bria/internal/nodecontrol"
	"github.com/Time4Mind/bria/internal/providerauth"
	"github.com/Time4Mind/bria/internal/runtimehost"
)

type nodeRuntimeControl struct {
	router            *nodecontrol.Router
	transcripts       *nodecontrol.TranscriptRouter
	sessionFiles      *nodecontrol.SessionFileRouter
	starts            *nodecontrol.StartRouter
	providerAuth      *nodecontrol.ProviderAuthRouter
	backendSetup      *nodecontrol.BackendSetupRouter
	speechSetup       *nodecontrol.SpeechSetupRouter
	updates           *nodeUpdateControl
	localProviderAuth *providerauth.Manager
	executor          *runtimehost.LocalExecutor
	store             *runtimehost.BoltOperationStore
	client            *nodecontrol.Client
	server            *nodecontrol.Server
	enrollment        *enrollmentRuntime
	listener          net.Listener
	errors            chan error
}
