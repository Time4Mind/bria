package sessionruntime

import "bria/internal/runtimediagnostic"

type startupDiagnostic struct{ runtimediagnostic.Drain }

func (d *startupDiagnostic) finish()              { d.Finish() }
func (d *startupDiagnostic) wrap(err error) error { return d.Wrap(err) }
func StartupFailureClass(err error) string        { return runtimediagnostic.FailureClass(err) }
