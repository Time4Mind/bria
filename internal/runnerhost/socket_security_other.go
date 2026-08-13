//go:build !linux

package runnerhost

import "errors"

func SocketOwnerUID(string) (int, error) {
	return -1, errors.New("isolated runner sockets are supported only on Linux")
}
