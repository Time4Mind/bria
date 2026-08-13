package telegramui

import (
	"fmt"
	"strings"

	"github.com/Time4Mind/bria/internal/i18n"
)

type ArchiveItem struct {
	Token    OpaqueToken
	Name     string
	NodeName string
	Index    int
}

type ArchiveListInput struct {
	Copy          i18n.Localizer
	Title         string
	Items         []ArchiveItem
	Page          int
	Pages         int
	Total         int
	PreviousToken OpaqueToken
	NextToken     OpaqueToken
}

type ArchiveNodeItem struct {
	Token    OpaqueToken
	Name     string
	Status   NodeStatus
	Archives int
}

func RenderArchiveNodes(copy i18n.Localizer, items []ArchiveNodeItem) Screen {
	rows := make(Grid, 0, len(items)+1)
	for _, item := range items {
		label := fmt.Sprintf("%s %s · %s", nodeStatusGlyph(item.Status), item.Name,
			copy.Count(i18n.CountArchivedSession, item.Archives))
		rows = append(rows, Row{button(label, ActionSelectArchiveNode, item.Token)})
	}
	if len(items) == 0 {
		rows = append(rows, Row{button(copy.Text(i18n.NoServers), ActionNoop, "")})
	}
	rows = append(rows, Row{button(copy.Text(i18n.ButtonBack), ActionMenu, "")})
	return Screen{Name: ScreenArchives, Text: copy.Text(i18n.ArchiveSelectTitle), Grid: rows}
}

// RenderArchives renders a pre-filtered node or all-host archive page.
func RenderArchives(input ArchiveListInput) Screen {
	page, pages := normalizedPages(input.Page, input.Pages)
	rows := make(Grid, 0, (len(input.Items)+1)/2+2)
	lines := make([]string, 0, len(input.Items)+2)
	lines = append(lines, input.Title,
		input.Copy.Format(i18n.ArchivePageLine, page, pages, input.Total))
	for index, item := range input.Items {
		if index%2 == 0 {
			rows = append(rows, Row{})
		}
		label := fmt.Sprintf("%d. %s", item.Index, item.Name)
		if item.NodeName != "" {
			label += " · " + item.NodeName
		}
		rows[len(rows)-1] = append(rows[len(rows)-1],
			button(label, ActionSelectArchive, item.Token))
		lines = append(lines, label)
	}
	if len(input.Items) == 0 {
		rows = append(rows, Row{button(input.Copy.Text(i18n.NoArchivedSessions), ActionNoop, "")})
	}
	navigation := Row{}
	if input.PreviousToken != "" {
		navigation = append(navigation, button("◀", ActionArchivePrevious, input.PreviousToken))
	}
	navigation = append(navigation, button(pageLabel(page, pages), ActionNoop, ""))
	if input.NextToken != "" {
		navigation = append(navigation, button("▶", ActionArchiveNext, input.NextToken))
	}
	rows = append(rows, navigation,
		Row{button(input.Copy.Text(i18n.ButtonBack), ActionMenu, "")})
	return Screen{Name: ScreenArchives, Text: strings.Join(lines, "\n"), Grid: rows}
}

type ArchiveInspectInput struct {
	Copy         i18n.Localizer
	Text         string
	RichMarkdown bool
	CanRestore   bool
	HasHistory   bool
	Tokens       map[Action]OpaqueToken
}

func RenderArchiveInspect(input ArchiveInspectInput) Screen {
	controls := Row{}
	if input.CanRestore {
		controls = append(controls,
			button(input.Copy.Text(i18n.ButtonRestore), ActionRestore, input.Tokens[ActionRestore]))
	}
	if input.HasHistory {
		controls = append(controls,
			button(input.Copy.Text(i18n.ButtonHistory), ActionArchiveHistory,
				input.Tokens[ActionArchiveHistory]))
	}
	controls = append(controls,
		button(input.Copy.Text(i18n.ButtonBack), ActionArchiveBack, input.Tokens[ActionArchiveBack]))
	return Screen{
		Name: ScreenArchives, Text: input.Text, RichMarkdown: input.RichMarkdown,
		Grid: Grid{controls},
	}
}

type ArchiveHistoryInput struct {
	Copy          i18n.Localizer
	Text          string
	RichMarkdown  bool
	Page          int
	Pages         int
	PreviousToken OpaqueToken
	NextToken     OpaqueToken
	BackToken     OpaqueToken
}

func RenderArchiveHistory(input ArchiveHistoryInput) Screen {
	page, pages := normalizedPages(input.Page, input.Pages)
	navigation := Row{}
	if input.PreviousToken != "" {
		navigation = append(navigation,
			button(input.Copy.Text(i18n.ButtonOlder), ActionHistoryPrevious, input.PreviousToken))
	}
	navigation = append(navigation, button(pageLabel(page, pages), ActionNoop, ""))
	if input.NextToken != "" {
		navigation = append(navigation,
			button(input.Copy.Text(i18n.ButtonNewer), ActionHistoryNext, input.NextToken))
	}
	return Screen{
		Name: ScreenArchives, Text: input.Text, RichMarkdown: input.RichMarkdown,
		Grid: Grid{
			navigation,
			Row{button(input.Copy.Text(i18n.ButtonBack), ActionSelectArchive, input.BackToken)},
		},
	}
}
