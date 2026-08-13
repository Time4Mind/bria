package clusterstate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/hashicorp/raft"
)

type FSM struct {
	machine *Machine
}

func NewFSM(machine *Machine) *FSM {
	return &FSM{machine: machine}
}

func (f *FSM) Machine() *Machine {
	return f.machine
}

func (f *FSM) Apply(logEntry *raft.Log) any {
	var command Command
	if err := json.Unmarshal(logEntry.Data, &command); err != nil {
		return Result{Error: fmt.Sprintf("decode raft command: %v", err)}
	}
	return f.machine.Apply(command)
}

func (f *FSM) Snapshot() (raft.FSMSnapshot, error) {
	data, err := f.machine.MarshalSnapshot()
	if err != nil {
		return nil, err
	}
	return &fsmSnapshot{data: data}, nil
}

func (f *FSM) Restore(reader io.ReadCloser) error {
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, 64<<20))
	if err != nil {
		return fmt.Errorf("read raft snapshot: %w", err)
	}
	return f.machine.RestoreSnapshot(data)
}

type fsmSnapshot struct {
	data []byte
}

func (s *fsmSnapshot) Persist(sink raft.SnapshotSink) error {
	if _, err := io.Copy(sink, bytes.NewReader(s.data)); err != nil {
		_ = sink.Cancel()
		return fmt.Errorf("write raft snapshot: %w", err)
	}
	if err := sink.Close(); err != nil {
		return fmt.Errorf("close raft snapshot: %w", err)
	}
	return nil
}

func (s *fsmSnapshot) Release() {}
