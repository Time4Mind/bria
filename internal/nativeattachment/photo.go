// Package nativeattachment validates photo custody before native CLI input.
package nativeattachment

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

var ErrInvalid = errors.New("native photo is unavailable or unsupported")

// ReadPhoto verifies bytes, not the filename suffix. Telegram custody uses
// extensionless payloads; only actual supported image containers are admitted.
func ReadPhoto(ctx context.Context, path string, size int64, digest string) ([]byte, string, error) {
	if ctx == nil || !filepath.IsAbs(path) || size < 1 || size > 32<<20 || len(digest) != 64 || !utf8.ValidString(path) {
		return nil, "", ErrInvalid
	}
	for _, r := range path {
		if r < 32 || r == 127 {
			return nil, "", ErrInvalid
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Size() != size {
		return nil, "", ErrInvalid
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, "", ErrInvalid
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, size+1))
	if err != nil || int64(len(data)) != size {
		return nil, "", ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || after.Size() != size {
		return nil, "", ErrInvalid
	}
	hash := sha256.Sum256(data)
	if hex.EncodeToString(hash[:]) != digest {
		return nil, "", ErrInvalid
	}
	ext, err := photoExtension(data)
	if err != nil {
		return nil, "", err
	}
	return data, ext, nil
}

func photoExtension(data []byte) (string, error) {
	if len(data) >= 20 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		if uint64(binary.LittleEndian.Uint32(data[4:8]))+8 != uint64(len(data)) {
			return "", ErrInvalid
		}
		kind := string(data[12:16])
		size := uint64(binary.LittleEndian.Uint32(data[16:20]))
		if (kind != "VP8 " && kind != "VP8L" && kind != "VP8X") || size == 0 || size > uint64(len(data)-20) {
			return "", ErrInvalid
		}
		return ".webp", nil
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return "", ErrInvalid
	}
	if format == "jpeg" {
		return ".jpg", nil
	}
	if format == "png" || format == "gif" {
		return "." + strings.ToLower(format), nil
	}
	return "", ErrInvalid
}
