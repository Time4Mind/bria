// Package nativereceiptstore owns private, exactly bound native receipt I/O.
// Outcomes and turn correlation always commit in one fsynced atomic document.
package nativereceiptstore

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"bria/internal/nativeacceptance"
)

func Path(root, sessionID string) (string, error) {
	if !filepath.IsAbs(root) || !utf8.ValidString(root) || strings.ContainsRune(root, 0) || sessionID == "" || sessionID == "." || sessionID == ".." || filepath.Base(sessionID) != sessionID || strings.ContainsAny(sessionID, "\\\x00\r\n") {
		return "", nativeacceptance.ErrInvalid
	}
	return filepath.Join(root, sessionID+".json"), nil
}

func Read(root, sessionID string) (nativeacceptance.Document, error) {
	var empty nativeacceptance.Document
	path, err := Path(root, sessionID)
	if err != nil {
		return empty, err
	}
	if _, err = privateDirectory(root); err != nil {
		return empty, err
	}
	before, err := os.Lstat(path)
	if err != nil {
		return empty, err
	}
	if !before.Mode().IsRegular() || before.Mode().Perm()&0077 != 0 || before.Size() > 1<<20 {
		return empty, nativeacceptance.ErrInvalid
	}
	file, err := os.Open(path)
	if err != nil {
		return empty, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return empty, nativeacceptance.ErrInvalid
	}
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil {
		return empty, err
	}
	doc, err := nativeacceptance.Decode(data, sessionID)
	if err != nil {
		return empty, err
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return empty, nativeacceptance.ErrInvalid
	}
	return doc, nil
}

func Write(root string, doc nativeacceptance.Document) error {
	path, err := Path(root, doc.SessionID)
	if err != nil {
		return err
	}
	data, err := nativeacceptance.Encode(doc)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return err
	}
	before, err := privateDirectory(root)
	if err != nil {
		return err
	}
	if target, err := os.Lstat(path); !os.IsNotExist(err) && (err != nil || !target.Mode().IsRegular() || target.Mode().Perm()&0077 != 0) {
		return nativeacceptance.ErrInvalid
	}
	file, err := os.CreateTemp(root, ".receipt-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err = file.Write(data); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if after, err := privateDirectory(root); err != nil || !os.SameFile(before, after) {
		return nativeacceptance.ErrInvalid
	}
	if err = os.Rename(file.Name(), path); err != nil {
		return err
	}
	directory, err := os.Open(root)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func privateDirectory(root string) (os.FileInfo, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return nil, nativeacceptance.ErrInvalid
	}
	return info, nil
}
