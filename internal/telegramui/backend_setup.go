package telegramui

import "github.com/Time4Mind/bria/internal/i18n"

type NodeBackendItem struct {
	Name      string
	Version   string
	Installed bool
	Connected bool
	Token     OpaqueToken
	OpenToken OpaqueToken
}

type NodeBackendsInput struct {
	Copy      i18n.Localizer
	NodeName  string
	Items     []NodeBackendItem
	BackToken OpaqueToken
}

type NodeBackendDetailInput struct {
	Copy       i18n.Localizer
	NodeName   string
	Backend    NodeBackendItem
	Alias      string
	AliasToken OpaqueToken
	AuthToken  OpaqueToken
	BackToken  OpaqueToken
}

func RenderNodeBackends(input NodeBackendsInput) Screen {
	rows := make(Grid, 0, len(input.Items)+1)
	for _, item := range input.Items {
		rows = append(rows, Row{button(
			item.Name+" · "+backendStatus(input.Copy, item), ActionNodeBackend, item.OpenToken,
		)})
	}
	rows = append(rows, Row{button(input.Copy.Text(i18n.ButtonBack), ActionNodeSettings, input.BackToken)})
	return Screen{
		Name: ScreenStatus, ParseMode: ParseModeHTML,
		Text: input.Copy.Format(i18n.BackendListTitle, input.NodeName), Grid: rows,
	}
}

func RenderNodeBackendDetail(input NodeBackendDetailInput) Screen {
	item := input.Backend
	rows := Grid{}
	action, label := ActionBackendInstall, input.Copy.Text(i18n.BackendInstall)
	if item.Installed {
		action, label = ActionBackendConnect, input.Copy.Text(i18n.BackendConnect)
	}
	if item.Connected {
		action, label = ActionBackendDisconnect, input.Copy.Text(i18n.BackendDisconnect)
	}
	rows = append(rows, Row{button(label, action, item.Token)})
	if item.Connected {
		alias := input.Copy.Format(i18n.ProviderAliasButton, item.Name)
		if input.Alias != "" {
			alias += ": " + input.Alias
		}
		rows = append(rows,
			Row{button(alias, ActionProviderAlias, input.AliasToken)},
			Row{button(input.Copy.Format(i18n.ProviderAuthButton, item.Name), ActionProviderAuth, input.AuthToken)},
		)
	}
	rows = append(rows, Row{button(input.Copy.Text(i18n.ButtonBack), ActionNodeBackends, input.BackToken)})
	return Screen{
		Name: ScreenStatus, ParseMode: ParseModeHTML,
		Text: input.Copy.Format(
			i18n.BackendDetailTitle, input.NodeName, item.Name, backendStatus(input.Copy, item),
		), Grid: rows,
	}
}

func backendStatus(copy i18n.Localizer, item NodeBackendItem) string {
	if item.Connected {
		return copy.Text(i18n.BackendStatusConnected)
	}
	if item.Installed {
		return copy.Text(i18n.BackendStatusInstalled)
	}
	return copy.Text(i18n.BackendStatusMissing)
}

func RenderBackendSetup(
	copy i18n.Localizer,
	text string,
	retry OpaqueToken,
	back OpaqueToken,
) Screen {
	rows := Grid{}
	if retry != "" {
		rows = append(rows, Row{button(copy.Text(i18n.BackendInstall), ActionBackendInstall, retry)})
	}
	rows = append(rows, Row{button(copy.Text(i18n.ButtonBack), ActionNodeBackend, back)})
	return Screen{Name: ScreenStatus, Text: text, ParseMode: ParseModeHTML, Grid: rows}
}
