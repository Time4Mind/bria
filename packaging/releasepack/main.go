// Command releasepack creates deterministic Bria release archives and their
// SHA-256 manifest from already cross-compiled staging directories.
package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func main() {
	input := flag.String("input", "", "directory containing one directory per platform bundle")
	output := flag.String("output", "", "empty release output directory")
	epoch := flag.Int64("source-date-epoch", 0, "deterministic Unix timestamp")
	flag.Parse()
	if flag.NArg() != 0 || *input == "" || *output == "" || *epoch < 0 {
		flag.Usage()
		os.Exit(2)
	}
	if err := packageBundles(*input, *output, time.Unix(*epoch, 0).UTC()); err != nil {
		fmt.Fprintf(os.Stderr, "releasepack: %v\n", err)
		os.Exit(1)
	}
}

func packageBundles(input, output string, timestamp time.Time) error {
	entries, err := os.ReadDir(input)
	if err != nil {
		return fmt.Errorf("read staging directory: %w", err)
	}
	if len(entries) == 0 {
		return errors.New("staging directory is empty")
	}
	if err := os.Mkdir(output, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}

	var archives []string
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			return fmt.Errorf("unexpected staging entry %q", entry.Name())
		}
		archive := entry.Name() + ".tar.gz"
		if err := writeArchive(filepath.Join(input, entry.Name()), filepath.Join(output, archive), entry.Name(), timestamp); err != nil {
			return err
		}
		archives = append(archives, archive)
	}
	sort.Strings(archives)
	return writeManifest(output, archives)
}

func writeArchive(source, destination, root string, timestamp time.Time) (returnErr error) {
	files, err := os.ReadDir(source)
	if err != nil {
		return fmt.Errorf("read bundle %q: %w", root, err)
	}
	if len(files) == 0 {
		return fmt.Errorf("bundle %q is empty", root)
	}
	for _, entry := range files {
		if entry.IsDir() || entry.Type()&fs.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("bundle %q contains unsupported entry %q", root, entry.Name())
		}
	}

	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create archive %q: %w", destination, err)
	}
	defer func() {
		if closeErr := file.Close(); returnErr == nil && closeErr != nil {
			returnErr = closeErr
		}
	}()

	gzipWriter, err := gzip.NewWriterLevel(file, gzip.BestCompression)
	if err != nil {
		return err
	}
	gzipWriter.Header.ModTime = timestamp
	gzipWriter.Header.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)

	sort.Slice(files, func(i, j int) bool { return files[i].Name() < files[j].Name() })
	for _, entry := range files {
		path := filepath.Join(source, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect %q: %w", path, err)
		}
		mode := int64(0o644)
		if info.Mode().Perm()&0o111 != 0 {
			mode = 0o755
		}
		header := &tar.Header{
			Name:    filepath.ToSlash(filepath.Join(root, entry.Name())),
			Mode:    mode,
			Size:    info.Size(),
			ModTime: timestamp,
			Uid:     0,
			Gid:     0,
			Uname:   "root",
			Gname:   "root",
			Format:  tar.FormatPAX,
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			return fmt.Errorf("write header for %q: %w", path, err)
		}
		sourceFile, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open %q: %w", path, err)
		}
		_, copyErr := io.Copy(tarWriter, sourceFile)
		closeErr := sourceFile.Close()
		if copyErr != nil {
			return fmt.Errorf("archive %q: %w", path, copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close %q: %w", path, closeErr)
		}
	}
	if err := tarWriter.Close(); err != nil {
		return fmt.Errorf("finish tar archive: %w", err)
	}
	if err := gzipWriter.Close(); err != nil {
		return fmt.Errorf("finish gzip archive: %w", err)
	}
	return nil
}

func writeManifest(output string, archives []string) (returnErr error) {
	manifestPath := filepath.Join(output, "SHA256SUMS")
	manifest, err := os.OpenFile(manifestPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create checksum manifest: %w", err)
	}
	defer func() {
		if closeErr := manifest.Close(); returnErr == nil && closeErr != nil {
			returnErr = closeErr
		}
	}()
	for _, archive := range archives {
		file, err := os.Open(filepath.Join(output, archive))
		if err != nil {
			return err
		}
		digest := sha256.New()
		_, copyErr := io.Copy(digest, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if _, err := fmt.Fprintf(manifest, "%x  %s\n", digest.Sum(nil), archive); err != nil {
			return err
		}
	}
	return nil
}
