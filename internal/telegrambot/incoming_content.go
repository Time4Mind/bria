package telegrambot

import "strings"

func contentFromAPIMessage(raw *apiMessage) (ContentDescriptor, string, []TextLink) {
	caption := strings.TrimSpace(raw.Caption)
	text := raw.Text
	quoteSalvaged := false
	if strings.TrimSpace(text) == "" && caption == "" && raw.Quote != nil {
		text = raw.Quote.Text
		quoteSalvaged = strings.TrimSpace(text) != ""
	}
	content := ContentDescriptor{Kind: IncomingText}
	switch {
	case raw.Voice != nil:
		content = contentFromVoice(raw.Voice)
	case len(raw.Photo) > 0:
		content = contentFromPhoto(largestPhoto(raw.Photo))
	case raw.Document != nil:
		content = contentFromDocument(raw.Document)
	case strings.TrimSpace(text) == "" && caption == "":
		return ContentDescriptor{}, "", nil
	}
	if strings.TrimSpace(text) == "" {
		text = caption
	}
	links := textLinks(raw.Text, raw.Entities)
	links = append(links, textLinks(raw.Caption, raw.CaptionEntities)...)
	if quoteSalvaged {
		links = append(links, textLinks(raw.Quote.Text, raw.Quote.Entities)...)
	}
	content.Caption = caption
	content.HiddenLinks = hiddenLinkURLs(links)
	origin := originFromAPI(raw.ForwardOrigin)
	return content, decorateIncomingText(text, origin, userFromAPI(raw.ViaBot), links), links
}

func contentFromVoice(voice *apiVoice) ContentDescriptor {
	if voice == nil {
		return ContentDescriptor{}
	}
	return ContentDescriptor{
		Kind: IncomingVoice, FileID: voice.FileID, FileUniqueID: voice.FileUniqueID,
		MIMEType: voice.MIMEType, FileSize: voice.FileSize, Duration: voice.Duration,
	}
}

func contentFromPhoto(photo *apiPhotoSize) ContentDescriptor {
	if photo == nil {
		return ContentDescriptor{}
	}
	return ContentDescriptor{
		Kind: IncomingPhoto, FileID: photo.FileID, FileUniqueID: photo.FileUniqueID,
		FileSize: photo.FileSize, Width: photo.Width, Height: photo.Height,
	}
}

func contentFromDocument(document *apiDocument) ContentDescriptor {
	if document == nil {
		return ContentDescriptor{}
	}
	return ContentDescriptor{
		Kind: IncomingDocument, FileID: document.FileID, FileUniqueID: document.FileUniqueID,
		FileName: document.FileName, MIMEType: document.MIMEType, FileSize: document.FileSize,
	}
}

func largestPhoto(photos []apiPhotoSize) *apiPhotoSize {
	if len(photos) == 0 {
		return nil
	}
	largest := &photos[0]
	for index := 1; index < len(photos); index++ {
		candidate := &photos[index]
		candidateArea := int64(candidate.Width) * int64(candidate.Height)
		largestArea := int64(largest.Width) * int64(largest.Height)
		if candidateArea > largestArea ||
			(candidateArea == largestArea && candidate.FileSize > largest.FileSize) {
			largest = candidate
		}
	}
	return largest
}

func externalReplyContent(raw *apiExternalReplyInfo) ContentDescriptor {
	switch {
	case raw.Voice != nil:
		return contentFromVoice(raw.Voice)
	case len(raw.Photo) > 0:
		return contentFromPhoto(largestPhoto(raw.Photo))
	case raw.Document != nil:
		return contentFromDocument(raw.Document)
	default:
		return ContentDescriptor{}
	}
}
