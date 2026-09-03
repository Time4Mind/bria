//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package instancelock

func acquirePlatform(string) (func() error, error) {
	return nil, ErrLockUnavailable
}
