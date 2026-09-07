package settingsport

// Model describes a provider-advertised selectable model, not a static product list.
type Model struct {
	ID            string
	Name          string
	Efforts       []string
	DefaultEffort string
	Default       bool
}
