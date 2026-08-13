package platform

import (
	"context"
	"errors"
	"testing"
)

func TestLinuxBootIDProvider(t *testing.T) {
	t.Parallel()
	want := "12345678-1234-4abc-8def-123456789abc"
	tests := []struct {
		name    string
		read    FileReaderFunc
		want    string
		wantErr bool
	}{
		{
			name: "canonical value",
			read: func(path string) ([]byte, error) {
				if path != linuxBootIDPath {
					t.Fatalf("path = %q, want %q", path, linuxBootIDPath)
				}
				return []byte("  " + want + "\n"), nil
			},
			want: want,
		},
		{
			name: "read failure",
			read: func(string) ([]byte, error) {
				return nil, errors.New("unmounted proc")
			},
			wantErr: true,
		},
		{
			name: "malformed value",
			read: func(string) ([]byte, error) {
				return []byte("not-a-boot-id"), nil
			},
			wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := NewLinuxBootIDProvider(test.read)
			got, err := provider.Current(context.Background())
			if (err != nil) != test.wantErr {
				t.Fatalf("Current() error = %v, wantErr %v", err, test.wantErr)
			}
			if got != test.want {
				t.Fatalf("Current() = %q, want %q", got, test.want)
			}
		})
	}
}
