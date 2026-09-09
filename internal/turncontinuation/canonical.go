package turncontinuation

// CanonicalMessage selects identity, never text, over the full journal snapshot.
// Completed members participate so a restart cannot promote a steer to root.
func CanonicalMessage(message, turn string, turns map[string]string, sequences map[string]uint64) string {
	if turn == "" {
		return message
	}
	root, sequence := message, sequences[message]
	if sequence == 0 || turns[message] != turn {
		return ""
	}
	for id, native := range turns {
		if seq := sequences[id]; native == turn && seq != 0 && seq < sequence {
			root, sequence = id, seq
		}
	}
	return root
}
