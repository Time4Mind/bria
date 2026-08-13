//go:build darwin

package platform

import "time"

func NewBootIDProvider() BootIDProvider {
	return NewDarwinBootIDProvider(ExecCommandRunner{}, 2*time.Second)
}
