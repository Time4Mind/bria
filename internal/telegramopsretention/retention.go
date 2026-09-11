// Package telegramopsretention bounds finalized opaque Telegram ledger history.
package telegramopsretention

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

type Snapshot struct {
	Operations       map[string]json.RawMessage
	Statuses         map[string]json.RawMessage
	Acknowledgements map[string]json.RawMessage
}

func Compact(state Snapshot, limit int) {
	for _, records := range []map[string]json.RawMessage{state.Operations, state.Statuses} {
		for id, raw := range records {
			if phase(raw) != "committed" {
				continue
			}
			var record map[string]json.RawMessage
			if json.Unmarshal(raw, &record) != nil {
				continue
			}
			delete(record, "prepared")
			if compacted, err := json.Marshal(record); err == nil {
				records[id] = compacted
			}
		}
	}
	callbackDependencies := nonFinalized(state.Statuses, "status")
	statusDependencies := nonFinalized(state.Operations, "callback")
	prune(state.Operations, "callback", callbackDependencies, limit)
	prune(state.Statuses, "status", statusDependencies, limit)
	prune(state.Acknowledgements, "acknowledgement", nil, limit)
}

func nonFinalized(records map[string]json.RawMessage, namespace string) map[string]bool {
	result := make(map[string]bool)
	for id, raw := range records {
		if !finalized(namespace, phase(raw)) {
			result[id] = true
		}
	}
	return result
}

func finalized(namespace, value string) bool {
	if namespace != "acknowledgement" {
		return value == "committed"
	}
	return value == "confirmed" || value == "failed" || value == "abandoned"
}

func prune(records map[string]json.RawMessage, namespace string, protected map[string]bool, limit int) {
	type item struct {
		id       string
		sequence uint64
	}
	protectedFinalized := 0
	candidates := make([]item, 0)
	for id, raw := range records {
		if !finalized(namespace, phase(raw)) {
			continue
		}
		if protected[id] {
			protectedFinalized++
			continue
		}
		candidates = append(candidates, item{id: id, sequence: sequence(id, raw)})
	}
	keep := limit - protectedFinalized
	if keep < 0 {
		keep = 0
	}
	if len(candidates) <= keep {
		return
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].sequence == candidates[j].sequence {
			return candidates[i].id < candidates[j].id
		}
		return candidates[i].sequence < candidates[j].sequence
	})
	for _, candidate := range candidates[:len(candidates)-keep] {
		delete(records, candidate.id)
	}
}

func phase(raw json.RawMessage) string {
	var value struct {
		Phase string `json:"phase"`
	}
	_ = json.Unmarshal(raw, &value)
	return value.Phase
}

func sequence(id string, raw json.RawMessage) uint64 {
	var value struct {
		Sequence uint64 `json:"sequence"`
		UpdateID int64  `json:"update_id"`
	}
	_ = json.Unmarshal(raw, &value)
	if value.Sequence > 0 {
		return value.Sequence
	}
	if value.UpdateID > 0 {
		return uint64(value.UpdateID)
	}
	for _, prefix := range []string{"status:", "recovery:callback:"} {
		if strings.HasPrefix(id, prefix) {
			parsed, _ := strconv.ParseUint(strings.TrimPrefix(id, prefix), 10, 64)
			return parsed
		}
	}
	return 0
}
