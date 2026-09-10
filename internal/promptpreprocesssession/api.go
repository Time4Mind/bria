package promptpreprocesssession

import (
	"bria/internal/promptpreprocess"
	"bria/internal/promptpreprocessbinding"
	"bria/internal/promptpreprocesscore"
)

type Mode = promptpreprocess.Mode

const (
	ModeDisabled   = promptpreprocess.ModeDisabled
	ModeShared     = promptpreprocess.ModeShared
	ModePerSession = promptpreprocess.ModePerSession
)

type DesiredState = promptpreprocessbinding.DesiredState

const (
	DesiredActive   = promptpreprocessbinding.DesiredActive
	DesiredArchived = promptpreprocessbinding.DesiredArchived
)

type BindingKey = promptpreprocessbinding.BindingKey
type Binding = promptpreprocessbinding.Binding
type BindingStore = promptpreprocessbinding.Store
type FileBindingStore = promptpreprocessbinding.FileBindingStore
type PrimaryState = promptpreprocesscore.PrimaryState
type StartRequest = promptpreprocesscore.StartRequest

var ErrBindingStore = promptpreprocessbinding.ErrBindingStore

func OpenFileBindingStore(path string) (*FileBindingStore, error) {
	return promptpreprocessbinding.OpenFileBindingStore(path)
}
