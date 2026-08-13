//go:build !linux && !darwin

package platform

func NewBootIDProvider() BootIDProvider {
	return UnsupportedBootIDProvider{}
}
