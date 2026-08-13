package telegramui

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/Time4Mind/bria/internal/i18n"
)

type CreateNodeItem struct {
	Token  OpaqueToken
	Name   string
	Status NodeStatus
}

type CreateChoice struct {
	Token   OpaqueToken
	Label   string
	Summary string
	Updated time.Time
}

func RenderCreateNodes(copy i18n.Localizer, items []CreateNodeItem) Screen {
	rows := make(Grid, 0, len(items)+1)
	for _, item := range items {
		label := fmt.Sprintf("%s %s", nodeStatusGlyph(item.Status), item.Name)
		if item.Status != NodeOnline {
			label += " · " + copy.Text(i18n.NodeUnavailable)
		}
		rows = append(rows, Row{button(label, ActionNewNode, item.Token)})
	}
	if len(items) == 0 {
		rows = append(rows, Row{button(copy.Text(i18n.NoServers), ActionNoop, "")})
	}
	rows = append(rows, Row{button(copy.Text(i18n.ButtonMenu), ActionMenu, "")})
	return Screen{Name: ScreenNodes, Text: copy.Text(i18n.NewSelectServer), Grid: rows}
}

func RenderCreateBackends(copy i18n.Localizer, node string, items []CreateChoice) Screen {
	return RenderCreateBackendsWithBack(copy, node, items, ActionNewSession, "")
}

func RenderCreateBackendsWithBack(
	copy i18n.Localizer,
	node string,
	items []CreateChoice,
	backAction Action,
	backToken OpaqueToken,
) Screen {
	rows := make(Grid, 0, len(items)+1)
	for _, item := range items {
		rows = append(rows, Row{button(item.Label, ActionNewBackend, item.Token)})
	}
	rows = append(rows, Row{button(copy.Text(i18n.ButtonBack), backAction, backToken)})
	return Screen{Name: ScreenSessions, Text: copy.Format(i18n.NewSelectBackend, node), Grid: rows}
}

func RenderCreateNoBackends(
	copy i18n.Localizer,
	node string,
	items []CreateChoice,
) Screen {
	rows := make(Grid, 0, len(items)+1)
	for _, item := range items {
		rows = append(rows, Row{button(
			"＋ "+item.Label+" · "+copy.Text(i18n.BackendConnect),
			ActionBackendConnect,
			item.Token,
		)})
	}
	rows = append(rows, Row{button(copy.Text(i18n.ButtonBack), ActionSessions, "")})
	return Screen{
		Name: ScreenSessions,
		Text: copy.Format(i18n.NewNoBackends, node),
		Grid: rows,
	}
}

func RenderCreateDirectories(
	copy i18n.Localizer,
	path string,
	items []CreateChoice,
	page, pages int,
) Screen {
	rows := make(Grid, 0, 7)
	for index := 0; index < len(items); index += 2 {
		row := Row{button("📁 "+items[index].Label, ActionNewDirectory, items[index].Token)}
		if index+1 < len(items) {
			row = append(row, button("📁 "+items[index+1].Label, ActionNewDirectory, items[index+1].Token))
		}
		rows = append(rows, row)
	}
	if len(items) == 0 {
		rows = append(rows, Row{button(copy.Text(i18n.NewNoDirectories), ActionNoop, "")})
	}
	if pages > 1 {
		rows = append(rows, Row{
			button("◀", ActionNewDirectoryPrev, ""),
			button(pageLabel(page, pages), ActionNewDirectoryFirst, ""),
			button("▶", ActionNewDirectoryNext, ""),
		})
	}
	rows = append(rows,
		Row{button(copy.Text(i18n.ButtonCancel), ActionSessions, ""),
			button("..", ActionNewDirectoryUp, ""),
			button(copy.Text(i18n.NewUseDirectory), ActionNewDirectoryPick, "")},
		Row{button(copy.Text(i18n.ButtonMenu), ActionMenu, "")},
	)
	return Screen{
		Name: ScreenSessions, Text: copy.Format(i18n.NewSelectDirectory, filepath.Clean(path)), Grid: rows,
	}
}

func RenderCreateResume(copy i18n.Localizer, workdir string, items []CreateChoice) Screen {
	return RenderCreateResumePage(copy, workdir, items, 0, 1, 1)
}

func RenderCreateResumePage(
	copy i18n.Localizer,
	workdir string,
	items []CreateChoice,
	offset int,
	page, pages int,
) Screen {
	rows := make(Grid, 0, len(items)+3)
	for index, item := range items {
		label := fmt.Sprintf("%d · %s", offset+index+1, item.Label)
		rows = append(rows, Row{button(label, ActionNewResume, item.Token)})
	}
	if pages > 1 {
		rows = append(rows, Row{
			button("◀", ActionNewResumePrevious, ""),
			button(pageLabel(page, pages), ActionNewResumeFirst, ""),
			button("▶", ActionNewResumeNext, ""),
		})
	}
	rows = append(rows,
		Row{button(copy.Text(i18n.NewStartFresh), ActionNewFresh, "")},
		Row{button(copy.Text(i18n.ButtonBack), ActionNewDirectoryBack, ""),
			button(copy.Text(i18n.ButtonMenu), ActionMenu, "")},
	)
	return Screen{Name: ScreenSessions, Text: copy.Format(i18n.NewSelectResume, workdir), Grid: rows}
}
