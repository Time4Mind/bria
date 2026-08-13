package nodecontrol

import "net/http"

func (s *Server) registerHandlers(mux *http.ServeMux) {
	mux.HandleFunc(executePath, s.handleExecute)
	mux.HandleFunc(lookupPath, s.handleLookup)
	mux.HandleFunc(heartbeatPath, s.handleHeartbeat)
	mux.HandleFunc(recoveryPath, s.handleRecovery)
	mux.HandleFunc(transcriptPath, s.handleTranscript)
	mux.HandleFunc(sessionFilePath, s.handleSessionFile)
	mux.HandleFunc(startBrowsePath, s.handleStartBrowse)
	mux.HandleFunc(startDiscoverPath, s.handleStartDiscover)
	mux.HandleFunc(startProvisionPath, s.handleStartProvision)
	mux.HandleFunc(providerAuthStartPath, s.handleProviderAuthStart)
	mux.HandleFunc(providerAuthSubmitPath, s.handleProviderAuthSubmit)
	mux.HandleFunc(providerAuthStatusPath, s.handleProviderAuthStatus)
	mux.HandleFunc(providerAuthCancelPath, s.handleProviderAuthCancel)
	mux.HandleFunc(speechSetupStartPath, s.handleSpeechSetupStart)
	mux.HandleFunc(speechSetupStatusPath, s.handleSpeechSetupStatus)
	s.registerUpdateHandlers(mux)
	mux.HandleFunc(enrollmentReportPath, s.handleEnrollment)
	mux.HandleFunc(healthPath, s.handleHealth)
	mux.HandleFunc(readinessPath, s.handleReadiness)
	mux.HandleFunc(metricsPath, s.handleMetrics)
	mux.HandleFunc(backupPath, s.handleBackup)
	mux.HandleFunc(membershipAdminPath, s.handleMembershipAdmin)
	mux.HandleFunc(membershipMovePath, s.handleMembershipRelocation)
}
