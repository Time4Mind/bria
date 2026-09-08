package sessionruntime

import "errors"

// Output EOF triggers the reaper. Its completion also drains the bounded
// diagnostic channel, so an exit cannot race ahead of its safe failure class.
func turnExitError(record *processRecord) error {
	<-record.done
	return record.startupDiagnostic.wrap(errors.New("provider process exited during turn"))
}

// RuntimeFailureClass returns a closed, payload-free category across the
// provider process and event-handler boundaries. Unknown errors stay unnamed.
func RuntimeFailureClass(err error) string {
	if class := StartupFailureClass(err); class != "" {
		return class
	}
	for _, failure := range []struct {
		err   error
		class string
	}{
		{ErrProtocol, "adapter_protocol_invalid"},
		{ErrEventHandler, "event_handler_failed"},
		{ErrInteractionHandler, "interaction_handler_failed"},
		{ErrInteractionAcceptanceHandler, "interaction_acceptance_failed"},
		{ErrTextTooLarge, "input_text_too_large"},
	} {
		if errors.Is(err, failure.err) {
			return failure.class
		}
	}
	return ""
}
