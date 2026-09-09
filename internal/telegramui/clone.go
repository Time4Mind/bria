package telegramui

// CloneCardKeyboard copies the keyboard's row containers.
func CloneCardKeyboard(keyboard CardKeyboard) CardKeyboard {
	rows := make([]ButtonRow, len(keyboard.Rows))
	for index, row := range keyboard.Rows {
		rows[index] = append(ButtonRow(nil), row...)
	}
	keyboard.Rows = rows
	return keyboard
}

// CloneCarrierProjection copies pages, anchors, keyboard rows and notification.
func CloneCarrierProjection(projection CarrierProjection) CarrierProjection {
	pages := make([]ContentPage, len(projection.Card.Pages))
	for index, page := range projection.Card.Pages {
		pages[index] = page
		pages[index].Anchors = append([]string(nil), page.Anchors...)
	}
	projection.Card.Pages = pages
	projection.Card.Keyboard = CloneCardKeyboard(projection.Card.Keyboard)
	if projection.Notification != nil {
		notification := *projection.Notification
		projection.Notification = &notification
	}
	return projection
}
