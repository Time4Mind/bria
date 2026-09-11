// Package nativeeventkind projects displayable native transcript kinds onto
// provider-neutral runtime event kinds.
package nativeeventkind

import "bria/internal/nativetranscript"

// Runtime returns the stable runtime kind for a displayable native event.
func Runtime(kind nativetranscript.Kind) string {
	switch kind {
	case nativetranscript.KindQuestion:
		return "question"
	case nativetranscript.KindTool:
		return "tool"
	case nativetranscript.KindThinking:
		return "thinking"
	default:
		return "commentary"
	}
}
