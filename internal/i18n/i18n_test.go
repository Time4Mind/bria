package i18n

import (
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
			if strings.Count(translated, "%s") != strings.Count(englishValue, "%s") {
				t.Errorf("%s/%s placeholder count differs", language, key)
			}
		}
		for key := range catalog {
			if _, ok := englishCatalog[key]; !ok {
				t.Errorf("%s has unknown key %s", language, key)
			}
		}
	}
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
