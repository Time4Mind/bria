//go:build !linux

package nativeterminal

import (
	"errors"
	"os/exec"
)

func pauseServer(*exec.Cmd) (func(), error) {
	return nil, errors.New("native terminal exact process cleanup unsupported")
}

type ownedProcess struct{}

func ownProcess(int) (*ownedProcess, error) {
	return nil, errors.New("native terminal exact process cleanup unsupported")
}
func (*ownedProcess) killTree() error {
	return errors.New("native terminal exact process cleanup unsupported")
}
