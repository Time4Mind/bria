package telegramui

import (
	"fmt"
	"html"
	"strings"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
)

type PendingEnrollmentItem struct {
	Name  string
	Token OpaqueToken
}

type EnrollmentDetailInput struct {
	Copy    i18n.Localizer
	Request domain.EnrollmentRequest
	Approve OpaqueToken
	Reject  OpaqueToken
}

func RenderEnrollmentMethods(copy i18n.Localizer) Screen {
	return Screen{
		Name: ScreenSettings, ParseMode: ParseModeHTML,
		Text: "<b>" + copy.Text(i18n.ClusterAddNode) + "</b>",
		Grid: Grid{
			Row{button(copy.Text(i18n.ClusterInvite), ActionClusterInvite, "")},
			Row{button(copy.Text(i18n.ClusterContract), ActionClusterContract, "")},
			Row{button(copy.Text(i18n.ButtonBack), ActionSettingsCategory, OpaqueToken(CategoryCluster))},
		},
	}
}

func RenderClusterInvitation(copy i18n.Localizer, invitation string) Screen {
	return Screen{
		Name: ScreenSettings, ParseMode: ParseModeHTML,
		Text: "<b>" + copy.Text(i18n.ClusterInvitationTitle) + "</b>\n\n" +
			copy.Text(i18n.ClusterInvitationBody) + "\n\n<code>" + html.EscapeString(invitation) + "</code>",
		Grid: Grid{Row{button(copy.Text(i18n.ButtonBack), ActionClusterAdd, "")}},
	}
}

func RenderNodeContractPrompt(copy i18n.Localizer) Screen {
	return Screen{
		Name: ScreenSettings, ParseMode: ParseModeHTML,
		Text: "<b>" + copy.Text(i18n.ClusterContract) + "</b>\n\n" +
			copy.Text(i18n.ClusterContractPrompt),
		Grid: Grid{Row{button(copy.Text(i18n.ButtonCancel), ActionSettingsCategory,
			OpaqueToken(CategoryCluster))}},
	}
}

func RenderEnrollmentDetail(input EnrollmentDetailInput) Screen {
	request := input.Request
	fingerprint := request.Fingerprint
	if len(fingerprint) > 24 {
		fingerprint = fingerprint[:24] + "…"
	}
	return Screen{
		Name: ScreenSettings, ParseMode: ParseModeHTML,
		Text: input.Copy.Format(i18n.ClusterEnrollmentDetail,
			html.EscapeString(request.Name), html.EscapeString(request.OS),
			html.EscapeString(request.Arch), html.EscapeString(request.Network.RaftAddress),
			html.EscapeString(fingerprint)),
		Grid: Grid{
			Row{
				button(input.Copy.Text(i18n.ClusterApprove), ActionEnrollmentApprove, input.Approve),
				button(input.Copy.Text(i18n.ClusterReject), ActionEnrollmentReject, input.Reject),
			},
			Row{button(input.Copy.Text(i18n.ButtonBack), ActionSettingsCategory, OpaqueToken(CategoryCluster))},
		},
	}
}

type NodeMembershipInput struct {
	Copy                  i18n.Localizer
	Node                  domain.Node
	Backends              string
	Status                NodeStatus
	LiveSessions          int
	CanDisable            bool
	DisableToken          OpaqueToken
	EnableToken           OpaqueToken
	DeleteToken           OpaqueToken
	RenameToken           OpaqueToken
	ProviderAliases       []ProviderAliasItem
	BackendChoices        []NodeBackendItem
	SpeechStatus          string
	SpeechToken           OpaqueToken
	CanManageIsolation    bool
	IsolationRequired     bool
	IsolationReady        bool
	IsolationCanRequire   bool
	IsolationMode         string
	IsolationRequireToken OpaqueToken
	IsolationAllowToken   OpaqueToken
	BackAction            Action
	BackToken             OpaqueToken
}

type NodeBackendItem struct {
	Name      string
	Version   string
	Connected bool
	Token     OpaqueToken
}

type ProviderAliasItem struct {
	Backend   string
	Alias     string
	Token     OpaqueToken
	AuthToken OpaqueToken
}

func RenderNodeMembership(input NodeMembershipInput) Screen {
	text := input.Copy.Format(i18n.StatusNodeSettings, html.EscapeString(input.Node.Name),
		html.EscapeString(input.Backends), nodeStatusGlyph(input.Status))
	if input.CanManageIsolation {
		isolation := input.Copy.Text(i18n.ValueOff)
		if input.IsolationReady {
			isolation = input.Copy.Format(i18n.NodeIsolationReady,
				html.EscapeString(input.IsolationMode))
		} else if input.IsolationRequired {
			isolation = input.Copy.Text(i18n.NodeIsolationMissing)
		}
		text += "\n" + input.Copy.Format(i18n.NodeIsolationStatus, isolation)
	}
	rows := Grid{}
	if input.Node.Enabled() && input.CanDisable {
		rows = append(rows, Row{button(input.Copy.Text(i18n.NodeDisable), ActionNodeDisable,
			input.DisableToken)})
	} else if !input.Node.Enabled() {
		rows = append(rows,
			Row{button(input.Copy.Text(i18n.NodeEnable), ActionNodeEnable, input.EnableToken)},
			Row{button(input.Copy.Text(i18n.NodeDelete), ActionNodeDelete, input.DeleteToken)},
		)
	}
	rows = append(rows, Row{button(input.Copy.Text(i18n.NodeRename), ActionNodeRename,
		input.RenameToken)})
	if input.CanManageIsolation {
		if input.IsolationRequired {
			rows = append(rows, Row{button(input.Copy.Text(i18n.NodeIsolationAllow),
				ActionNodeIsolationAllow, input.IsolationAllowToken)})
		} else if !input.IsolationCanRequire {
			rows = append(rows, Row{button(input.Copy.Text(i18n.NodeIsolationCloseSessions),
				ActionNoop, "")})
		} else {
			rows = append(rows, Row{button(input.Copy.Text(i18n.NodeIsolationRequire),
				ActionNodeIsolationRequire, input.IsolationRequireToken)})
		}
	}
	if input.SpeechStatus != "" {
		rows = append(rows, Row{button(
			input.Copy.Format(i18n.NodeSpeechStatus, input.SpeechStatus),
			ActionNodeSpeechSetup, input.SpeechToken,
		)})
	}
	for _, backend := range input.BackendChoices {
		label := "＋ " + backend.Name
		action := ActionBackendConnect
		if backend.Connected {
			label = "✓ " + backend.Name + " · " + input.Copy.Text(i18n.BackendDisconnect)
			action = ActionBackendDisconnect
		} else {
			label += " · " + input.Copy.Text(i18n.BackendConnect)
		}
		rows = append(rows, Row{button(label, action, backend.Token)})
	}
	for _, provider := range input.ProviderAliases {
		label := input.Copy.Format(i18n.ProviderAliasButton, provider.Backend)
		if provider.Alias != "" {
			label += ": " + provider.Alias
		}
		rows = append(rows, Row{button(label, ActionProviderAlias, provider.Token)})
		rows = append(rows, Row{button(
			input.Copy.Format(i18n.ProviderAuthButton, provider.Backend),
			ActionProviderAuth, provider.AuthToken,
		)})
	}
	backAction, backToken := input.BackAction, input.BackToken
	if backAction == "" {
		backAction, backToken = ActionStatusMode, "settings"
	}
	rows = append(rows, Row{button(input.Copy.Text(i18n.ButtonBack), backAction, backToken)})
	return Screen{Name: ScreenStatus, Text: text, ParseMode: ParseModeHTML, Grid: rows}
}

func RenderNodeRenamePrompt(copy i18n.Localizer, name string) Screen {
	return RenderNodeRenamePromptWithBack(copy, name, ActionStatusMode, "settings")
}

func RenderNodeRenamePromptWithBack(
	copy i18n.Localizer, name string, backAction Action, backToken OpaqueToken,
) Screen {
	return Screen{
		Name: ScreenStatus, ParseMode: ParseModeHTML,
		Text: copy.Format(i18n.NodeRenamePrompt, html.EscapeString(name)),
		Grid: Grid{Row{button(copy.Text(i18n.ButtonCancel), backAction, backToken)}},
	}
}

func RenderProviderAliasPrompt(copy i18n.Localizer, backend string) Screen {
	return RenderProviderAliasPromptWithBack(copy, backend, ActionStatusMode, "settings")
}

func RenderProviderAliasPromptWithBack(
	copy i18n.Localizer, backend string, backAction Action, backToken OpaqueToken,
) Screen {
	return Screen{
		Name: ScreenStatus, ParseMode: ParseModeHTML,
		Text: copy.Format(i18n.ProviderAliasPrompt, html.EscapeString(backend)),
		Grid: Grid{Row{button(copy.Text(i18n.ButtonCancel), backAction, backToken)}},
	}
}

func RenderProviderAuthStarting(copy i18n.Localizer, backend string) Screen {
	return RenderProviderAuthStartingWithBack(copy, backend, ActionStatusMode, "settings")
}

func RenderProviderAuthStartingWithBack(
	copy i18n.Localizer, backend string, backAction Action, backToken OpaqueToken,
) Screen {
	return Screen{
		Name: ScreenStatus, ParseMode: ParseModeHTML,
		Text: copy.Format(i18n.ProviderAuthStarting, html.EscapeString(backend)),
		Grid: Grid{Row{button(copy.Text(i18n.ButtonBack), backAction, backToken)}},
	}
}

func RenderProviderAuthChallenge(
	copy i18n.Localizer,
	backend string,
	url string,
	userCode string,
	wantsInput bool,
	cancelToken OpaqueToken,
) Screen {
	key := i18n.ProviderAuthDevice
	if wantsInput {
		key = i18n.ProviderAuthPaste
	}
	return Screen{
		Name: ScreenStatus, ParseMode: ParseModeHTML,
		Text: copy.Format(key, html.EscapeString(backend), html.EscapeString(url),
			html.EscapeString(userCode)),
		Grid: Grid{Row{button(copy.Text(i18n.ButtonCancel), ActionProviderAuthCancel, cancelToken)}},
	}
}

func RenderNodeDisableConfirmation(input NodeMembershipInput, confirm OpaqueToken) Screen {
	text := input.Copy.Format(i18n.NodeDisableConfirm, html.EscapeString(input.Node.Name))
	if input.LiveSessions > 0 {
		text = input.Copy.Format(i18n.NodeDisableSessionsConfirm,
			html.EscapeString(input.Node.Name), input.LiveSessions)
	}
	return membershipConfirmation(input.Copy, text, ActionNodeDisableYes, confirm,
		input.BackAction, input.BackToken)
}

func RenderNodeDeleteConfirmation(input NodeMembershipInput, confirm OpaqueToken) Screen {
	text := input.Copy.Format(i18n.NodeDeleteConfirm, html.EscapeString(input.Node.Name))
	return membershipConfirmation(input.Copy, text, ActionNodeDeleteYes, confirm,
		input.BackAction, input.BackToken)
}

func membershipConfirmation(
	copy i18n.Localizer,
	text string,
	action Action,
	token OpaqueToken,
	backAction Action,
	backToken OpaqueToken,
) Screen {
	if backAction == "" {
		backAction, backToken = ActionStatusMode, "settings"
	}
	return Screen{
		Name: ScreenStatus, ParseMode: ParseModeHTML, Text: strings.TrimSpace(text),
		Grid: Grid{
			Row{button(copy.Text(i18n.ButtonConfirm), action, token)},
			Row{button(copy.Text(i18n.ButtonCancel), backAction, backToken)},
		},
	}
}

func RenderMembershipResult(copy i18n.Localizer, key i18n.Key, detail string) Screen {
	return RenderMembershipResultWithBack(
		copy, key, detail, ActionSettingsCategory, OpaqueToken(CategoryCluster),
	)
}

func RenderMembershipResultWithBack(
	copy i18n.Localizer,
	key i18n.Key,
	detail string,
	backAction Action,
	backToken OpaqueToken,
) Screen {
	text := copy.Text(key)
	if detail != "" {
		text = copy.Format(key, detail)
	}
	return Screen{
		Name: ScreenSettings, ParseMode: ParseModeHTML, Text: fmt.Sprintf("%s", html.EscapeString(text)),
		Grid: Grid{Row{button(copy.Text(i18n.ButtonBack), backAction, backToken)}},
	}
}

func RenderEnrollmentClaim(copy i18n.Localizer, claim string) Screen {
	return Screen{
		Name: ScreenSettings, ParseMode: ParseModeHTML,
		Text: copy.Text(i18n.EnrollmentContractClaim) + "\n\n<code>" +
			html.EscapeString(claim) + "</code>",
		Grid: Grid{Row{button(copy.Text(i18n.ButtonBack), ActionSettingsCategory,
			OpaqueToken(CategoryCluster))}},
	}
}
