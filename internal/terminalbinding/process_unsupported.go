//go:build !linux && !darwin

package terminalbinding

import (
	"errors"
	"os/exec"
)

func PauseServer(*exec.Cmd) (func(), error) {
	return nil, errors.New("native terminal exact process cleanup unsupported")
}

type Process struct{}

func IsolateServer(*exec.Cmd) {}
func (*Process) Release()     {}

func OwnProcess(int) (*Process, error) {
	return nil, errors.New("native terminal exact process cleanup unsupported")
}
func (*Process) KillTree() error {
	return errors.New("native terminal exact process cleanup unsupported")
}
