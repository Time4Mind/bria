package config

import "path/filepath"

func (c Config) EffectiveUpdateInstallRoot() string {
	if c.UpdateInstallRoot != "" {
		return c.UpdateInstallRoot
	}
	return filepath.Join(c.DataDir, "software")
}
