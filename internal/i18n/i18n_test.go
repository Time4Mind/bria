package i18n

import (
	"regexp"
	"strings"
	"testing"
)

func TestCatalogsMatchEnglishKeysAndPlaceholders(t *testing.T) {
	for language, catalog := range catalogs {
		for key, englishValue := range englishCatalog {
			translated, ok := catalog[key]
			if !ok || strings.TrimSpace(translated) == "" {
				t.Errorf("%s is missing %s", language, key)
				continue
			}
			if got, want := formatPlaceholders(translated), formatPlaceholders(englishValue); !equalStrings(got, want) {
				t.Errorf("%s/%s placeholders = %v, want %v", language, key, got, want)
			}
		}
		for key := range catalog {
			if _, ok := englishCatalog[key]; !ok {
				t.Errorf("%s has unknown key %s", language, key)
			}
		}
	}
}

var formatPlaceholder = regexp.MustCompile(`%(?:%|s|d)`)

func formatPlaceholders(value string) []string {
	return formatPlaceholder.FindAllString(value, -1)
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func TestCountCatalogsAreComplete(t *testing.T) {
	for language, catalog := range countCatalogs {
		for key := range englishCounts {
			forms, ok := catalog[key]
			if !ok || strings.TrimSpace(forms.other) == "" {
				t.Errorf("%s is missing count %s", language, key)
			}
		}
	}
}

func TestRussianPluralForms(t *testing.T) {
	copy := For("ru")
	wants := map[int]string{
		1: "1 день", 2: "2 дня", 5: "5 дней", 21: "21 день",
	}
	for count, want := range wants {
		if got := copy.Count(CountDay, count); got != want {
			t.Errorf("CountDay(%d)=%q, want %q", count, got, want)
		}
	}
}

func TestFallbackIsEnglishAndUnknownKeyIsVisible(t *testing.T) {
	copy := For("de")
	if copy.Language() != "en" || copy.Text(MenuTitle) != "Menu" {
		t.Fatalf("unknown locale fallback=%q/%q", copy.Language(), copy.Text(MenuTitle))
	}
	if got := copy.Text(Key("missing.key")); got != "missing.key" {
		t.Fatalf("missing key=%q", got)
	}
}
