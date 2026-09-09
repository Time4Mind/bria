// Package turncontinuation plans observation of existing provider acceptance.
package turncontinuation

import (
	"bria/internal/turnprocessing"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"
)

type Member struct {
	Input     turnprocessing.DurableLeasedInput
	TurnID    string
	Accepted  bool
	Callbacks turnprocessing.DurableInputCallbacks
}
type Group struct{ Members []Member }

func Plan(members []Member) ([]Group, error) {
	if len(members) == 0 || len(members) > 512 {
		return nil, errors.New("invalid continuation batch size")
	}
	ordered := append([]Member(nil), members...)
	messages, sequences := map[string]bool{}, map[uint64]bool{}
	for i, m := range ordered {
		in := m.Input
		if in.SessionID == "" || in.SessionID != ordered[0].Input.SessionID || !validID(in.MessageID) || in.Sequence == 0 || messages[in.MessageID] || sequences[in.Sequence] || m.Callbacks.OnCompleted == nil || m.TurnID != "" && !validID(m.TurnID) || m.TurnID == "" && (len(members) != 1 || !m.Accepted) {
			return nil, errors.New("invalid or ambiguous continuation identity")
		}
		messages[in.MessageID], sequences[in.Sequence] = true, true
		// Observation needs identity and attachment receipts, never prompt bytes.
		ordered[i].Input.Payload = nil
		ordered[i].Input.Attachments = append([]turnprocessing.AttachmentRef(nil), in.Attachments...)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Input.Sequence < ordered[j].Input.Sequence })
	var groups []Group
	index := map[string]int{}
	for _, m := range ordered {
		i, ok := index[m.TurnID]
		if !ok {
			i = len(groups)
			index[m.TurnID] = i
			groups = append(groups, Group{})
		}
		groups[i].Members = append(groups[i].Members, m)
	}
	return groups, nil
}
func validID(id string) bool {
	return id != "" && len(id) <= 1024 && strings.TrimSpace(id) == id && utf8.ValidString(id)
}
