package telegramapp

import (
	"errors"

	"github.com/Time4Mind/bria/internal/providerauth"
	"github.com/Time4Mind/bria/internal/speechsetup"
)

func (h *Handler) SetProviderAuth(service providerauth.Service) error {
	if service == nil {
		return errors.New("provider authentication service is required")
	}
	h.providerAuth = service
	return nil
}

func (h *Handler) SetSpeechSetup(service speechsetup.Service) error {
	if service == nil {
		return errors.New("speech setup service is required")
	}
	h.speechSetup = service
	return nil
}
