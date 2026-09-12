package screen

import (
	"context"

	"bria/internal/nativerender"
)

const (
	DefaultNativeCaptureKiB = nativerender.DefaultNativeCaptureKiB
	NativeMaxPNGBytes       = nativerender.NativeMaxPNGBytes
	ImageProfileCurrent     = nativerender.ImageProfileCurrent
	ImageProfileFull8       = nativerender.ImageProfileFull8
	ImageProfileCompact8    = nativerender.ImageProfileCompact8
)

var ErrInvalidNativeOptions = nativerender.ErrInvalidNativeOptions

// NativeOptions preserves the native screenshot API for screen consumers.
type NativeOptions = nativerender.NativeOptions
type ImageProfile = nativerender.ImageProfile

// RenderNative rasterizes the selected terminal capture without accessing a PTY.
func RenderNative(ctx context.Context, text string) ([]byte, error) {
	return nativerender.RenderNative(ctx, text)
}

// RenderNativeWithOptions renders the newest bounded ANSI terminal payload.
func RenderNativeWithOptions(ctx context.Context, text string, options NativeOptions) ([]byte, error) {
	return nativerender.RenderNativeWithOptions(ctx, text, options)
}
