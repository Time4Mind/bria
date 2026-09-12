package nativerender

import "bria/internal/pngprofile"

func applyNativeImageProfile(data []byte, profile ImageProfile) ([]byte, error) {
	output, err := pngprofile.Apply(data, profile)
	if err == pngprofile.ErrInvalidProfile {
		return nil, ErrInvalidNativeOptions
	}
	return output, err
}
