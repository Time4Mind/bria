package nativeadapter

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

type receiptDocument struct {
	SessionID string            `json:"session_id"`
	Receipts  map[string]string `json:"receipts"`
}

func (a *adapter) receiptPath() (string, error) {
	if !filepath.IsAbs(a.config.StateDir) || filepath.Base(a.id) != a.id || a.id == "" {
		return "", errors.New("native receipt identity invalid")
	}
	return filepath.Join(a.config.StateDir, a.id+".json"), nil
}
func (a *adapter) loadReceipts() error {
	path, err := a.receiptPath()
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 1<<20 {
		return errors.New("native receipt file invalid")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return errors.New("native receipt file exceeds bound")
	}
	var d receiptDocument
	if json.Unmarshal(data, &d) != nil || d.SessionID != a.id || len(d.Receipts) > 10000 {
		return errors.New("native receipt binding invalid")
	}
	for key, value := range d.Receipts {
		if key == "" || len(key) > 1024 || (value != "unknown" && value != "completed" && value != "failed") {
			return errors.New("native receipt entry invalid")
		}
	}
	a.receipts = d.Receipts
	if a.receipts == nil {
		a.receipts = map[string]string{}
	}
	return nil
}
func (a *adapter) saveReceipts() error {
	path, err := a.receiptPath()
	if err != nil {
		return err
	}
	delete(a.receipts, "")
	if len(a.receipts) > 10000 {
		return errors.New("native receipt capacity exceeded")
	}
	data, err := json.Marshal(receiptDocument{SessionID: a.id, Receipts: a.receipts})
	if err != nil {
		return err
	}
	if err = os.MkdirAll(a.config.StateDir, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(a.config.StateDir, ".receipt-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	directory, err := os.Open(a.config.StateDir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
