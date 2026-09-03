// Package workdir validates confirmed local working directories.
package workdir

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// ExistingDirectory validates an operating-system directory without rewriting
// the exact path confirmed by the caller.
type ExistingDirectory struct{}

func (ExistingDirectory) Validate(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("workdir path must be absolute: %q", path)
	}

	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat workdir %q: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("workdir %q is not a directory", path)
	}
	return nil
}
