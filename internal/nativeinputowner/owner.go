// Package nativeinputowner selects the latest applied input for a native turn.
package nativeinputowner

// Latest returns the latest accepted, incomplete candidate for one exact turn.
// State returns turn identity, accepted and completed for candidate index.
func Latest(turn string, count int, state func(int) (string, bool, bool)) int {
	if turn == "" || count <= 0 || state == nil {
		return -1
	}
	for index := count - 1; index >= 0; index-- {
		candidateTurn, accepted, completed := state(index)
		if accepted && !completed && candidateTurn == turn {
			return index
		}
	}
	return -1
}
