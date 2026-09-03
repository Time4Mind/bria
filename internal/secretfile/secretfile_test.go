package secretfile

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestUseProvidesFreshSecretAndWipesItAfterCallback(t *testing.T) {
	path := writeSecret(t, "correct horse")
	options := Options{MaxBytes: 32, MinBytes: 8}
	var retained []byte
	called := false

	if err := Use(path, options, func(secret []byte) error {
		called = true
		retained = secret
		if cap(secret) != len(secret) || cap(secret) > HardMaxBytes {
			t.Fatalf("callback slice len/cap = %d/%d", len(secret), cap(secret))
		}
		if got := string(secret); got != "correct horse" {
			t.Fatalf("secret = %q", got)
		}
		secret[0] = 'X'
		return nil
	}); err != nil {
		t.Fatalf("Use() error = %v", err)
	}
	if !called {
		t.Fatal("callback was not called")
	}
	assertZeroed(t, retained)

	if err := Use(path, options, func(secret []byte) error {
		if got := string(secret); got != "correct horse" {
			t.Fatalf("second secret = %q", got)
		}
		return nil
	}); err != nil {
		t.Fatalf("second Use() error = %v", err)
	}
}

func TestUseNormalizesOneFinalNewlineOnlyWhenRequested(t *testing.T) {
	tests := []struct {
		name string
		body string
		trim bool
		want string
	}{
		{name: "disabled", body: "secret\n", want: "secret\n"},
		{name: "LF", body: "secret\n", trim: true, want: "secret"},
		{name: "CRLF", body: "secret\r\n", trim: true, want: "secret"},
		{name: "one only", body: "secret\n\n", trim: true, want: "secret\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := writeSecret(t, test.body)
			var got string
			err := Use(path, Options{MaxBytes: 32, MinBytes: 1, TrimFinalNewline: test.trim}, func(secret []byte) error {
				if cap(secret) != len(secret) {
					t.Fatalf("callback slice len/cap = %d/%d", len(secret), cap(secret))
				}
				got = string(secret)
				return nil
			})
			if err != nil {
				t.Fatalf("Use() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("secret = %q, want %q", got, test.want)
			}
		})
	}
}

func TestUseRejectsUnsafePathsAndFiles(t *testing.T) {
	secure := writeSecret(t, "secret")
	directory := t.TempDir()
	badMode := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(badMode, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	relative := "relative-secret"
	unclean := filepath.Dir(secure) + string(filepath.Separator) + "unused" +
		string(filepath.Separator) + ".." + string(filepath.Separator) + filepath.Base(secure)

	tests := []struct {
		name string
		path string
		want error
	}{
		{name: "relative", path: relative, want: ErrInvalidPath},
		{name: "unclean", path: unclean, want: ErrInvalidPath},
		{name: "directory", path: directory, want: ErrUnsafeFile},
		{name: "mode", path: badMode, want: ErrUnsafeFile},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := false
			err := Use(test.path, Options{MaxBytes: 32, MinBytes: 1}, func([]byte) error {
				called = true
				return nil
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if called {
				t.Fatal("callback called for unsafe file")
			}
			if strings.Contains(err.Error(), test.path) {
				t.Fatal("error disclosed path")
			}
		})
	}
}

func TestUseRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation commonly requires privileges on Windows")
	}
	target := writeSecret(t, "secret")
	link := filepath.Join(t.TempDir(), "secret-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	err := Use(link, Options{MaxBytes: 32, MinBytes: 1}, func([]byte) error { return nil })
	if !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("error = %v, want ErrUnsafeFile", err)
	}
}

func TestUseRejectsInvalidBoundsShortAndOversize(t *testing.T) {
	path := writeSecret(t, "12345")
	tests := []struct {
		name    string
		options Options
		want    error
	}{
		{name: "zero maximum", options: Options{MinBytes: 1}, want: ErrInvalidOptions},
		{name: "above hard maximum", options: Options{MaxBytes: HardMaxBytes + 1, MinBytes: 1}, want: ErrInvalidOptions},
		{name: "zero minimum", options: Options{MaxBytes: 5}, want: ErrInvalidOptions},
		{name: "minimum exceeds maximum", options: Options{MaxBytes: 5, MinBytes: 6}, want: ErrInvalidOptions},
		{name: "too short", options: Options{MaxBytes: 5, MinBytes: 6}, want: ErrInvalidOptions},
		{name: "oversize", options: Options{MaxBytes: 4, MinBytes: 1}, want: ErrTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := Use(path, test.options, func([]byte) error { return nil })
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}

	short := writeSecret(t, "1234")
	err := Use(short, Options{MaxBytes: 8, MinBytes: 5}, func([]byte) error { return nil })
	if !errors.Is(err, ErrTooShort) {
		t.Fatalf("short error = %v, want ErrTooShort", err)
	}
}

func TestUseWipesSecretOnCallbackErrorAndDoesNotExposeCause(t *testing.T) {
	path := writeSecret(t, "top-secret")
	var retained []byte
	err := Use(path, Options{MaxBytes: 32, MinBytes: 1}, func(secret []byte) error {
		retained = secret
		return errors.New("top-secret at /sensitive/path")
	})
	if !errors.Is(err, ErrCallback) || strings.Contains(err.Error(), "top-secret") || strings.Contains(err.Error(), "/sensitive/path") {
		t.Fatalf("error = %q, want content-free ErrCallback", err)
	}
	assertZeroed(t, retained)
}

func TestUseWipesSecretWhenCallbackPanics(t *testing.T) {
	path := writeSecret(t, "panic-secret")
	var retained []byte
	func() {
		defer func() {
			if recovered := recover(); recovered != "boom" {
				t.Fatalf("recovered = %v", recovered)
			}
		}()
		_ = Use(path, Options{MaxBytes: 32, MinBytes: 1}, func(secret []byte) error {
			retained = secret
			panic("boom")
		})
	}()
	assertZeroed(t, retained)
}

func TestUseRejectsFileSwappedBetweenInspectionAndOpen(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "secret")
	replacement := filepath.Join(directory, "replacement")
	if err := os.WriteFile(path, []byte("first-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replacement, []byte("other-secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := use(path, Options{MaxBytes: 32, MinBytes: 1}, func([]byte) error {
		t.Fatal("callback called after path swap")
		return nil
	}, func() {
		if err := os.Rename(replacement, path); err != nil {
			t.Fatal(err)
		}
	})
	if !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("error = %v, want ErrUnsafeFile", err)
	}
}

func TestUseRejectsSymlinkToSameFileIntroducedAfterInspection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation commonly requires privileges on Windows")
	}
	directory := t.TempDir()
	path := filepath.Join(directory, "secret")
	moved := filepath.Join(directory, "moved")
	if err := os.WriteFile(path, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := use(path, Options{MaxBytes: 32, MinBytes: 1}, func([]byte) error {
		t.Fatal("callback called through raced symlink")
		return nil
	}, func() {
		if err := os.Rename(path, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(moved, path); err != nil {
			t.Fatal(err)
		}
	})
	if !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("error = %v, want ErrUnsafeFile", err)
	}
}

func TestUseRechecksModeAfterOpen(t *testing.T) {
	path := writeSecret(t, "secret")
	err := use(path, Options{MaxBytes: 32, MinBytes: 1}, func([]byte) error {
		t.Fatal("callback called after mode change")
		return nil
	}, func() {
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
	})
	if !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("error = %v, want ErrUnsafeFile", err)
	}
}

func writeSecret(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertZeroed(t *testing.T, content []byte) {
	t.Helper()
	for index, value := range content {
		if value != 0 {
			t.Fatalf("byte %d was not wiped", index)
		}
	}
}
