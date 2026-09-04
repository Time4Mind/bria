package telegramnodes

import (
	"bria/internal/domain"
	"bria/internal/sessioncreation"
)

type Button struct {
	Label  string
	Choice int
}

func Menu(current domain.ComputerID, nodes []sessioncreation.Computer) [][]Button {
	rows := make([][]Button, 0, len(nodes))
	for index, node := range nodes {
		label := node.Name
		if node.Coordinator {
			label += " · координатор"
		}
		if node.ID == current {
			label = "✓ " + label
		}
		if !node.Available {
			label += " · недоступна"
		}
		rows = append(rows, []Button{{Label: label, Choice: index + 1}})
	}
	return rows
}
