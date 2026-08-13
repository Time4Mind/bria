package i18n

var catalogs = map[string]map[Key]string{
	english: englishCatalog,
	russian: russianCatalog,
	chinese: chineseCatalog,
}

var countCatalogs = map[string]map[CountKey]countForms{
	english: englishCounts,
	russian: russianCounts,
	chinese: chineseCounts,
}

func Catalogs() map[string]map[Key]string {
	copyCatalogs := make(map[string]map[Key]string, len(catalogs))
	for language, catalog := range catalogs {
		copyCatalog := make(map[Key]string, len(catalog))
		for key, value := range catalog {
			copyCatalog[key] = value
		}
		copyCatalogs[language] = copyCatalog
	}
	return copyCatalogs
}
