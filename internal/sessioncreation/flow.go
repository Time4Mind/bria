// Package sessioncreation owns one ephemeral new-session flow.
package sessioncreation

import (
	"errors"
	"strings"
	"sync"
	"unicode/utf8"

	"bria/internal/domain"
)

const ChoicesPerPage = 8

type Step string

const (
	StepComputer        Step = "computer"
	StepProvider        Step = "provider"
	StepInstallRequired Step = "install_required"
	StepDirectory       Step = "directory"
	StepDirectoryName   Step = "directory_name"
	StepRecommendation  Step = "recommendation"
	StepReady           Step = "ready"
	StepUnavailable     Step = "unavailable"
)

type Draft struct {
	ComputerID domain.ComputerID
	Provider   domain.Provider
	Workdir    string
}

type Recommendation struct {
	SessionID domain.SessionID
	Label     string
}

type Defaults struct {
	Providers              map[domain.ComputerID]domain.Provider
	Workdirs               map[domain.ComputerID]string
	ArchiveRecommendations bool
}

type Snapshot struct {
	Draft                  Draft
	Step                   Step
	Computers              []Computer
	Providers              []ProviderCapability
	CurrentDirectory       string
	Directories            []Directory
	Recommendations        []Recommendation
	Page                   int
	Pages                  int
	ArchiveRecommendations bool
	ValidationError        string
	AwaitingWorkdir        bool
}

type Flow struct {
	mu              sync.Mutex
	draft           *Draft
	step            Step
	shown           []Step
	computers       []Computer
	providers       []ProviderCapability
	currentDir      string
	directories     []Directory
	recommendations []Recommendation
	page            int
	recommend       bool
	errText         string
	revision        uint64
	legacyWaiting   bool
}

func New() *Flow { return &Flow{} }

func (flow *Flow) Revision() uint64 {
	flow.mu.Lock()
	defer flow.mu.Unlock()
	return flow.revision
}

func (flow *Flow) BeginV2(computers []Computer, defaults Defaults) Snapshot {
	flow.mu.Lock()
	defer flow.mu.Unlock()
	flow.resetLocked(computers, defaults)
	return flow.snapshotLocked()
}

func (flow *Flow) CurrentV2(computers []Computer, defaults Defaults) (Snapshot, bool) {
	flow.mu.Lock()
	defer flow.mu.Unlock()
	if flow.draft == nil {
		return Snapshot{}, false
	}
	flow.computers = cloneComputers(computers)
	computer, ok := findComputer(flow.computers, flow.draft.ComputerID)
	if flow.draft.ComputerID != "" && !ok {
		flow.resetLocked(computers, defaults)
		flow.errText = "выбранный компьютер больше недоступен"
		return flow.snapshotLocked(), true
	}
	if ok {
		flow.providers = append([]ProviderCapability(nil), computer.Capabilities...)
		if flow.draft.Provider != "" && !enabledProvider(flow.draft.Provider, flow.providers) {
			flow.draft.Provider = ""
			flow.draft.Workdir = ""
			flow.currentDir = ""
			flow.directories = nil
			flow.recommendations = nil
			enabled := enabledProviders(flow.providers)
			if len(enabled) == 1 {
				flow.draft.Provider = enabled[0]
				flow.draft.Workdir = strings.TrimSpace(defaults.Workdirs[flow.draft.ComputerID])
				flow.enterLocked(StepDirectory, true)
			} else {
				flow.enterLocked(providerStep(flow.providers), true)
			}
			flow.errText = "выбранный бэкенд больше недоступен"
		} else if flow.draft.Provider == "" && flow.step == StepProvider {
			enabled := enabledProviders(flow.providers)
			if len(enabled) == 1 {
				flow.draft.Provider = enabled[0]
				flow.draft.Workdir = strings.TrimSpace(defaults.Workdirs[flow.draft.ComputerID])
				flow.enterLocked(StepDirectory, true)
				flow.errText = ""
			}
		}
	}
	return flow.snapshotLocked(), true
}

func (flow *Flow) SelectComputer(choice int, defaults Defaults) error {
	flow.mu.Lock()
	defer flow.mu.Unlock()
	if flow.draft == nil || flow.step != StepComputer || choice < 1 || choice > len(flow.computers) {
		return errors.New("session creation computer choice is unavailable")
	}
	flow.chooseComputerLocked(flow.computers[choice-1], defaults)
	return nil
}

func (flow *Flow) SelectProviderV2(provider domain.Provider, defaults Defaults) error {
	flow.mu.Lock()
	defer flow.mu.Unlock()
	if flow.draft == nil || !enabledProvider(provider, flow.providers) {
		return errors.New("session creation provider is unavailable")
	}
	flow.draft.Provider = provider
	flow.draft.Workdir = strings.TrimSpace(defaults.Workdirs[flow.draft.ComputerID])
	flow.currentDir = ""
	flow.directories = nil
	flow.recommendations = nil
	flow.errText = ""
	flow.legacyWaiting = false
	flow.enterLocked(StepDirectory, true)
	return nil
}

func (flow *Flow) DefaultWorkdir() string {
	flow.mu.Lock()
	defer flow.mu.Unlock()
	if flow.draft == nil {
		return ""
	}
	return flow.draft.Workdir
}

func (flow *Flow) SetDirectoryListing(current string, directories []Directory) error {
	flow.mu.Lock()
	defer flow.mu.Unlock()
	if flow.draft == nil || flow.draft.ComputerID == "" || flow.draft.Provider == "" {
		return errors.New("session creation target is incomplete")
	}
	flow.currentDir = current
	flow.draft.Workdir = ""
	flow.directories = append([]Directory(nil), directories...)
	flow.page = 0
	flow.errText = ""
	if flow.step == StepDirectoryName {
		if len(flow.shown) > 0 && flow.shown[len(flow.shown)-1] == StepDirectoryName {
			flow.shown = flow.shown[:len(flow.shown)-1]
		}
		flow.step = StepDirectory
	} else {
		flow.enterLocked(StepDirectory, true)
	}
	return nil
}

func (flow *Flow) SetDirectoryError(message string) {
	flow.mu.Lock()
	defer flow.mu.Unlock()
	flow.errText = strings.TrimSpace(message)
	flow.enterLocked(StepDirectory, false)
}

func (flow *Flow) DirectoryChoice(choice int) (Directory, error) {
	flow.mu.Lock()
	defer flow.mu.Unlock()
	items := visible(flow.directories, flow.page)
	if flow.step != StepDirectory || choice < 1 || choice > len(items) {
		return Directory{}, errors.New("session creation directory choice is unavailable")
	}
	return items[choice-1], nil
}

func (flow *Flow) SelectWorkdir(path string) error {
	flow.mu.Lock()
	defer flow.mu.Unlock()
	if flow.draft == nil || flow.step != StepDirectory || strings.TrimSpace(path) == "" {
		return errors.New("session creation workdir is unavailable")
	}
	flow.draft.Workdir = path
	flow.errText = ""
	if flow.recommend {
		flow.enterLocked(StepRecommendation, true)
	} else {
		flow.enterLocked(StepReady, false)
	}
	return nil
}

func (flow *Flow) BeginDirectoryName() error {
	flow.mu.Lock()
	defer flow.mu.Unlock()
	if flow.draft == nil || flow.step != StepDirectory || flow.currentDir == "" {
		return errors.New("session creation directory parent is unavailable")
	}
	flow.errText = ""
	flow.enterLocked(StepDirectoryName, true)
	return nil
}

func (flow *Flow) ConsumeDirectoryName(text string, hasMedia bool) (string, bool) {
	flow.mu.Lock()
	defer flow.mu.Unlock()
	if flow.draft == nil || flow.step != StepDirectoryName {
		return "", false
	}
	flow.revision++
	name := strings.TrimSpace(text)
	if hasMedia || !validFlowChildName(name) {
		flow.errText = "нужно отдельное текстовое имя дочерней папки"
		return "", true
	}
	flow.errText = ""
	return name, true
}

func (flow *Flow) CurrentDirectory() string {
	flow.mu.Lock()
	defer flow.mu.Unlock()
	return flow.currentDir
}

func (flow *Flow) SetRecommendations(items []Recommendation) error {
	flow.mu.Lock()
	defer flow.mu.Unlock()
	if flow.draft == nil || flow.draft.Workdir == "" {
		return errors.New("session creation workdir is unavailable")
	}
	flow.recommendations = append([]Recommendation(nil), items...)
	flow.page = 0
	if len(items) == 0 {
		flow.enterLocked(StepReady, false)
	} else {
		flow.enterLocked(StepRecommendation, true)
	}
	return nil
}

func (flow *Flow) SkipRecommendations() error {
	flow.mu.Lock()
	defer flow.mu.Unlock()
	if flow.draft == nil || flow.draft.Workdir == "" {
		return errors.New("session creation workdir is unavailable")
	}
	flow.recommendations = nil
	flow.enterLocked(StepReady, false)
	return nil
}

func (flow *Flow) RecommendationChoice(choice int) (Recommendation, error) {
	flow.mu.Lock()
	defer flow.mu.Unlock()
	items := visible(flow.recommendations, flow.page)
	if flow.step != StepRecommendation || choice < 1 || choice > len(items) {
		return Recommendation{}, errors.New("session creation recommendation choice is unavailable")
	}
	return items[choice-1], nil
}

func (flow *Flow) Page(delta int, first bool) Snapshot {
	flow.mu.Lock()
	defer flow.mu.Unlock()
	pages := flow.pagesLocked()
	if first || pages <= 1 {
		flow.page = 0
	} else {
		flow.page = (flow.page + delta + pages) % pages
	}
	return flow.snapshotLocked()
}

func (flow *Flow) Back() (Snapshot, bool) {
	flow.mu.Lock()
	defer flow.mu.Unlock()
	if flow.draft == nil || len(flow.shown) < 2 {
		return flow.snapshotLocked(), false
	}
	flow.shown = flow.shown[:len(flow.shown)-1]
	flow.step = flow.shown[len(flow.shown)-1]
	flow.page = 0
	flow.errText = ""
	if flow.step == StepDirectory {
		flow.recommendations = nil
		flow.draft.Workdir = ""
	}
	return flow.snapshotLocked(), true
}

func (flow *Flow) Ready() (Draft, bool) {
	flow.mu.Lock()
	defer flow.mu.Unlock()
	if flow.draft == nil || flow.step != StepReady || flow.draft.ComputerID == "" || flow.draft.Provider == "" || flow.draft.Workdir == "" {
		return Draft{}, false
	}
	return *flow.draft, true
}

func (flow *Flow) Cancel() {
	flow.mu.Lock()
	defer flow.mu.Unlock()
	flow.draft = nil
	flow.step = ""
	flow.shown = nil
	flow.computers = nil
	flow.providers = nil
	flow.currentDir = ""
	flow.directories = nil
	flow.recommendations = nil
	flow.page = 0
	flow.errText = ""
	flow.legacyWaiting = false
}

func (flow *Flow) resetLocked(computers []Computer, defaults Defaults) {
	flow.draft = &Draft{}
	flow.computers = cloneComputers(computers)
	flow.providers = nil
	flow.currentDir = ""
	flow.directories = nil
	flow.recommendations = nil
	flow.page = 0
	flow.recommend = defaults.ArchiveRecommendations
	flow.errText = ""
	flow.legacyWaiting = false
	flow.shown = nil
	if len(flow.computers) == 0 {
		flow.enterLocked(StepUnavailable, true)
		return
	}
	if len(flow.computers) > 1 {
		flow.enterLocked(StepComputer, true)
		return
	}
	flow.chooseComputerLocked(flow.computers[0], defaults)
}

func (flow *Flow) chooseComputerLocked(computer Computer, defaults Defaults) {
	flow.draft.ComputerID = computer.ID
	flow.draft.Provider = ""
	flow.draft.Workdir = ""
	flow.providers = append([]ProviderCapability(nil), computer.Capabilities...)
	flow.currentDir = ""
	flow.directories = nil
	flow.recommendations = nil
	flow.errText = ""
	enabled := enabledProviders(flow.providers)
	if configured := defaults.Providers[computer.ID]; configured != "" && enabledProvider(configured, flow.providers) {
		flow.draft.Provider = configured
	} else if len(enabled) == 1 {
		flow.draft.Provider = enabled[0]
	}
	if flow.draft.Provider == "" {
		flow.enterLocked(providerStep(flow.providers), true)
		return
	}
	flow.draft.Workdir = strings.TrimSpace(defaults.Workdirs[computer.ID])
	flow.enterLocked(StepDirectory, true)
}

func providerStep(capabilities []ProviderCapability) Step {
	for _, capability := range capabilities {
		if capability.Installed {
			return StepProvider
		}
	}
	return StepInstallRequired
}

func (flow *Flow) enterLocked(step Step, shown bool) {
	if flow.step == step {
		return
	}
	flow.step = step
	flow.page = 0
	if shown && (len(flow.shown) == 0 || flow.shown[len(flow.shown)-1] != step) {
		flow.shown = append(flow.shown, step)
	}
}

func (flow *Flow) pagesLocked() int {
	count := 0
	if flow.step == StepDirectory {
		count = len(flow.directories)
	}
	if flow.step == StepRecommendation {
		count = len(flow.recommendations)
	}
	if count == 0 {
		return 1
	}
	return (count + ChoicesPerPage - 1) / ChoicesPerPage
}

func (flow *Flow) snapshotLocked() Snapshot {
	if flow.draft == nil {
		return Snapshot{}
	}
	pages := flow.pagesLocked()
	if flow.page >= pages {
		flow.page = pages - 1
	}
	return Snapshot{
		Draft: *flow.draft, Step: flow.step,
		Computers: cloneComputers(flow.computers), Providers: append([]ProviderCapability(nil), flow.providers...),
		CurrentDirectory: flow.currentDir, Directories: append([]Directory(nil), visible(flow.directories, flow.page)...),
		Recommendations: append([]Recommendation(nil), visible(flow.recommendations, flow.page)...),
		Page:            flow.page + 1, Pages: pages, ArchiveRecommendations: flow.recommend,
		ValidationError: flow.errText,
		AwaitingWorkdir: flow.legacyWaiting,
	}
}

// Begin keeps the former text-path flow available while controller migration
// is in progress; production v2 entry uses BeginV2.
func (flow *Flow) Begin(local domain.ComputerID, providers []domain.Provider, sessions []domain.Session, fallbackWorkdir string) Snapshot {
	capabilities := make([]ProviderCapability, 0, len(providers))
	for _, provider := range providers {
		capabilities = append(capabilities, ProviderCapability{Provider: provider, Installed: true, Enabled: true})
	}
	defaults := Defaults{Providers: map[domain.ComputerID]domain.Provider{}, Workdirs: map[domain.ComputerID]string{}}
	var latest domain.Session
	for _, session := range sessions {
		if session.ComputerID() == local && (latest.CreatedAt().IsZero() || session.CreatedAt().After(latest.CreatedAt())) {
			latest = session
		}
	}
	if !latest.CreatedAt().IsZero() {
		defaults.Workdirs[local] = latest.Workdir()
		defaults.Providers[local] = latest.Provider()
	} else if strings.TrimSpace(fallbackWorkdir) != "" {
		defaults.Workdirs[local] = fallbackWorkdir
	}
	return flow.BeginV2([]Computer{{ID: local, Name: string(local), Capabilities: capabilities}}, defaults)
}

func (flow *Flow) Current(providers []domain.Provider) (Snapshot, bool) {
	capabilities := make([]ProviderCapability, 0, len(providers))
	for _, provider := range providers {
		capabilities = append(capabilities, ProviderCapability{Provider: provider, Installed: true, Enabled: true})
	}
	flow.mu.Lock()
	if flow.draft == nil {
		flow.mu.Unlock()
		return Snapshot{}, false
	}
	computer := flow.draft.ComputerID
	flow.mu.Unlock()
	return flow.CurrentV2([]Computer{{ID: computer, Name: string(computer), Capabilities: capabilities}}, Defaults{})
}

func (flow *Flow) SelectProvider(provider domain.Provider, available []domain.Provider) error {
	capabilities := make([]ProviderCapability, 0, len(available))
	for _, candidate := range available {
		capabilities = append(capabilities, ProviderCapability{Provider: candidate, Installed: true, Enabled: true})
	}
	flow.mu.Lock()
	workdir := flow.draft.Workdir
	flow.providers = capabilities
	flow.mu.Unlock()
	if err := flow.SelectProviderV2(provider, Defaults{}); err != nil {
		return err
	}
	flow.mu.Lock()
	flow.draft.Workdir = workdir
	flow.mu.Unlock()
	return nil
}

func (flow *Flow) BeginWorkdir() error {
	flow.mu.Lock()
	defer flow.mu.Unlock()
	if flow.draft == nil {
		return errors.New("session creation draft is not open")
	}
	flow.legacyWaiting = true
	return nil
}

func (flow *Flow) ConsumeWorkdir(text string, hasMedia bool) bool {
	flow.mu.Lock()
	defer flow.mu.Unlock()
	if flow.draft == nil || !flow.legacyWaiting {
		return false
	}
	flow.revision++
	workdir := strings.TrimSpace(text)
	if hasMedia || !strings.HasPrefix(workdir, "/") {
		flow.errText = "путь должен быть абсолютным"
		return true
	}
	flow.draft.Workdir = workdir
	flow.legacyWaiting = false
	flow.errText = ""
	flow.step = StepReady
	return true
}

func (flow *Flow) Confirm(available []domain.Provider) (Draft, error) {
	flow.mu.Lock()
	defer flow.mu.Unlock()
	if flow.draft == nil || flow.legacyWaiting || flow.draft.ComputerID == "" || flow.draft.Provider == "" || flow.draft.Workdir == "" {
		return Draft{}, errors.New("session creation draft is incomplete")
	}
	for _, provider := range available {
		if provider == flow.draft.Provider {
			return *flow.draft, nil
		}
	}
	return Draft{}, errors.New("session creation provider is unavailable")
}

func findComputer(computers []Computer, id domain.ComputerID) (Computer, bool) {
	for _, computer := range computers {
		if computer.ID == id {
			return computer, true
		}
	}
	return Computer{}, false
}

func enabledProviders(capabilities []ProviderCapability) []domain.Provider {
	result := make([]domain.Provider, 0, len(capabilities))
	for _, capability := range capabilities {
		if capability.Installed && capability.Enabled {
			result = append(result, capability.Provider)
		}
	}
	return result
}

func enabledProvider(provider domain.Provider, capabilities []ProviderCapability) bool {
	for _, capability := range capabilities {
		if capability.Provider == provider && capability.Installed && capability.Enabled {
			return true
		}
	}
	return false
}

func cloneComputers(source []Computer) []Computer {
	result := append([]Computer(nil), source...)
	for index := range result {
		result[index].Capabilities = append([]ProviderCapability(nil), result[index].Capabilities...)
	}
	return result
}

func visible[T any](items []T, page int) []T {
	start := min(len(items), page*ChoicesPerPage)
	return items[start:min(len(items), start+ChoicesPerPage)]
}

func validFlowChildName(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 255 || !utf8.ValidString(name) ||
		strings.TrimSpace(name) != name || strings.ContainsAny(name, "/\\") {
		return false
	}
	for _, character := range name {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}
