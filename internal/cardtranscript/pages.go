package cardtranscript

import "strconv"

type Page struct {
	Content string
	Anchors []string
}

// Paginate packs rendered blocks without losing semantic final boundaries.
// A final and its continuations never share pages with any other block.
func Paginate(blocks []Block, maxPages int) []Page {
	if maxPages < 1 {
		maxPages = 1
	}
	const pageBytes = 3000
	pages := make([]Page, 0, maxPages)
	current := Page{}
	appendCurrent := func() {
		if current.Content == "" {
			return
		}
		if len(pages) == maxPages {
			copy(pages, pages[1:])
			pages[len(pages)-1] = current
		} else {
			pages = append(pages, current)
		}
		current = Page{}
	}
	for index, block := range blocks {
		if block.Text == "" {
			continue
		}
		if block.Kind == "final" {
			appendCurrent()
		}
		parts := Split(block.Text, pageBytes)
		for partIndex, part := range parts {
			separator := ""
			if current.Content != "" {
				separator = Separator
			}
			if current.Content != "" && len(current.Content)+len(separator)+len(part) > pageBytes {
				appendCurrent()
				separator = ""
			}
			current.Content += separator + part
			anchor := "history:" + strconv.Itoa(index+1)
			if len(parts) > 1 {
				anchor += ":part:" + strconv.Itoa(partIndex+1)
			}
			current.Anchors = append(current.Anchors, anchor)
			if block.Kind == "final" || len(current.Content) == pageBytes {
				appendCurrent()
			}
		}
	}
	appendCurrent()
	if len(pages) == 0 {
		return []Page{{Anchors: []string{"empty"}}}
	}
	return pages
}
