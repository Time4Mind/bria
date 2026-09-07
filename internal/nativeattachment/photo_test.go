package nativeattachment

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestReadPhotoRecognizesRealImageBytesWithoutSuffix(t *testing.T) {
	for _, format := range []string{"png", "jpg", "gif"} {
		t.Run(format, func(t *testing.T) {
			var data bytes.Buffer
			img := image.NewRGBA(image.Rect(0, 0, 2, 2))
			var err error
			switch format {
			case "png":
				err = png.Encode(&data, img)
			case "jpg":
				err = jpeg.Encode(&data, img, nil)
			case "gif":
				err = gif.Encode(&data, img, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "photo")
			if err := os.WriteFile(path, data.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			digest := fmt.Sprintf("%x", sha256.Sum256(data.Bytes()))
			got, ext, err := ReadPhoto(context.Background(), path, int64(data.Len()), digest)
			if err != nil || ext != "."+format || !bytes.Equal(got, data.Bytes()) {
				t.Fatalf("format=%s ext=%s err=%v", format, ext, err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, _, err := ReadPhoto(ctx, path, int64(data.Len()), digest); err == nil {
				t.Fatal("canceled photo read succeeded")
			}
			alias := filepath.Join(filepath.Dir(path), "alias")
			if err := os.Symlink(path, alias); err == nil {
				if _, _, err := ReadPhoto(context.Background(), alias, int64(data.Len()), digest); err == nil {
					t.Fatal("symlink photo admitted")
				}
			}
		})
	}
}

func TestUnsupportedAndSpoofedPhotosFailClosed(t *testing.T) {
	for _, data := range [][]byte{[]byte("plain text"), []byte("%PDF-1.7"), []byte("OggS fake audio"), []byte("RIFF\x00\x00\x00\x00WEBPVP8 fake")} {
		if _, err := photoExtension(data); err == nil {
			t.Fatalf("unsupported photo admitted: %q", data)
		}
	}
}
