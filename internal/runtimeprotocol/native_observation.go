package runtimeprotocol

func validateNativeObservation(message AdapterMessage, limits Limits) error {
	if !validRequiredText(message.ProviderSessionID, 1024) || !validText(message.Text, min(limits.MaxTextBytes, 24<<10)) || !validNativeHash(message.Hash) || !validOptionalDisplayName(message.Model, 256) || message.RequestID != "" || message.Kind != "" || message.MessageID != "" || message.Status != "" || message.ErrorCode != "" || message.ProviderSessionName != "" || message.Readiness != "" || message.Authentication != "" || message.InteractionRequest != nil || message.InteractionID != "" {
		return ErrProtocol
	}
	// Hash identifies the complete native screen; Text can be a cropped picker.
	return nil
}
