package i18n

import "fmt"

type CountKey string

const (
	CountLiveSession     CountKey = "count.live_session"
	CountArchivedSession CountKey = "count.archived_session"
	CountBackend         CountKey = "count.backend"
	CountHour            CountKey = "count.hour"
	CountDay             CountKey = "count.day"
	CountMinute          CountKey = "count.minute"
)

type Localizer struct {
	language string
	catalog  map[Key]string
	counts   map[CountKey]countForms
}

func For(language string) Localizer {
	normalized := normalize(language)
	return Localizer{language: normalized, catalog: catalogs[normalized], counts: countCatalogs[normalized]}
}

func (l Localizer) Language() string { return l.language }

func (l Localizer) Text(key Key) string {
	if value, ok := l.catalog[key]; ok {
		return value
	}
	if value, ok := catalogs[english][key]; ok {
		return value
	}
	return string(key)
}

func (l Localizer) Format(key Key, values ...any) string {
	return fmt.Sprintf(l.Text(key), values...)
}

func (l Localizer) Count(key CountKey, count int) string {
	forms, ok := l.counts[key]
	if !ok {
		forms = countCatalogs[english][key]
	}
	return fmt.Sprintf(forms.choose(l.language, count), count)
}

func normalize(language string) string {
	switch language {
	case russian:
		return russian
	case chinese:
		return chinese
	default:
		return english
	}
}

type countForms struct {
	one, few, many, other string
}

func (f countForms) choose(language string, count int) string {
	if language == russian {
		lastTwo, last := count%100, count%10
		if last == 1 && lastTwo != 11 {
			return f.one
		}
		if last >= 2 && last <= 4 && (lastTwo < 12 || lastTwo > 14) {
			return f.few
		}
		return f.many
	}
	if language == english && count == 1 {
		return f.one
	}
	return f.other
}
