package clusterupdate

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
)

func sameRuntimeReleasePayload(left, right string) (bool, error) {
	leftNames, err := runtimeReleaseNames(left)
	if err != nil {
		return false, err
	}
	rightNames, err := runtimeReleaseNames(right)
	if err != nil || len(leftNames) != len(rightNames) {
		return false, err
	}
	for index, name := range leftNames {
		if rightNames[index] != name {
			return false, nil
		}
		leftFile, err := os.Open(filepath.Join(left, name))
		if err != nil {
			return false, err
		}
		rightFile, err := os.Open(filepath.Join(right, name))
		if err != nil {
			_ = leftFile.Close()
			return false, err
		}
		equal, compareErr := equalBoundedFiles(leftFile, rightFile)
		leftCloseErr, rightCloseErr := leftFile.Close(), rightFile.Close()
		if compareErr != nil {
			return false, compareErr
		}
		if leftCloseErr != nil || rightCloseErr != nil {
			return false, errors.New("close release payload")
		}
		if !equal {
			return false, nil
		}
	}
	return true, nil
}

func runtimeReleaseNames(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Name() == "release.json" {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxExtractedReleaseBytes {
			return nil, errors.New("release payload is not a bounded regular file")
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names, nil
}

func equalBoundedFiles(left, right *os.File) (bool, error) {
	leftInfo, err := left.Stat()
	if err != nil {
		return false, err
	}
	rightInfo, err := right.Stat()
	if err != nil || leftInfo.Size() != rightInfo.Size() {
		return false, err
	}
	leftBuffer, rightBuffer := make([]byte, 64<<10), make([]byte, 64<<10)
	for {
		leftN, leftErr := left.Read(leftBuffer)
		rightN, rightErr := right.Read(rightBuffer)
		if leftN != rightN || !bytes.Equal(leftBuffer[:leftN], rightBuffer[:rightN]) {
			return false, nil
		}
		if leftErr == io.EOF && rightErr == io.EOF {
			return true, nil
		}
		if leftErr != nil || rightErr != nil {
			return false, errors.New("read release payload")
		}
	}
}
