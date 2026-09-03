// Command check_repo validates Bria repository policy and architecture.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type requiredSection struct {
	name          string
	conceptGroups [][]string
}

var requiredSections = []requiredSection{
	{name: "Project boundary", conceptGroups: [][]string{
		{"текущее состояние", "current state"},
		{"только файлы этого репозитория", "this repository is the sole authority"},
		{"до 2026-09-02", "before 2026-09-02"},
		{"память и встроенные сессии", "memory and agent sessions"},
		{"bria-legacy"},
		{"не могут использоваться как текущее состояние", "must not be used as current state"},
	}},
	{name: "Task contract", conceptGroups: [][]string{
		{"source", "источник"},
		{"acceptance", "приемк", "приёмк", "готов"},
	}},
	{name: "Coverage map", conceptGroups: [][]string{
		{"owner", "владел"},
		{"stop", "останов"},
	}},
	{name: "Parallel execution", conceptGroups: [][]string{
		{"parallel", "параллел", "одновремен"},
		{"maximum", "максим"},
	}},
	{name: "Ownership and conflicts", conceptGroups: [][]string{
		{"file", "файл"},
		{"conflict", "конфликт"},
	}},
	{name: "Integration owner", conceptGroups: [][]string{
		{"integrat", "объедин"},
		{"evidence", "доказ", "свидетельств"},
	}},
	{name: "Verification", conceptGroups: [][]string{
		{"test", "проверк"},
		{"output", "результат"},
	}},
	{name: "Safety and writes", conceptGroups: [][]string{
		{"write", "изменен", "запис"},
		{"confirm", "подтвержд"},
	}},
	{name: "Scaling rules", conceptGroups: [][]string{
		{"scal", "масштаб"},
		{"split", "раздел", "decompos", "декомпоз"},
	}},
}

var mandatoryInvariants = []string{
	"единый неизменный договор задачи до начала работы;",
	"карта покрытия до параллельного запуска;",
	"максимальная безопасная параллельность и непересекающееся владение;",
	"отдельные рабочие копии для конфликтующих изменений;",
	"единственный владелец объединения;",
	"проверка доказательств и явное описание неопределённости;",
	"быстрые профильные проверки во время разработки и полный набор перед выпуском;",
	"проверка физического результата;",
	"точная bounded последовательность и точные цели с terminal criterion, одно явное подтверждение на действие или bounded sequence, действующее до terminal criterion и включающее необходимые commit, push и CI-fix iterations, выполнение только согласованного scope, повторное чтение после каждого существенного внешнего write и новое подтверждение для новых, расширенных или destructive targets, deploy, изменений secrets и исходящих messages.",
}

// requiredPolicyClauses protects the operational meaning of the policy. Short
// keyword checks alone are insufficient: removing a limit, an ownership rule,
// or a verification step must fail even when the rest of the section still
// contains words such as "owner" or "test".
var requiredPolicyClauses = map[string][]string{
	"Task contract": {
		"Перед любым многошаговым исследованием или изменением письменно зафиксировать единый договор задачи:",
		"- точный результат;",
		"- источник требований и доказательств;",
		"- период и часовой пояс, если они влияют на результат;",
		"- единицу результата, охватываемые случаи и исключения;",
		"- разрешённые изменения;",
		"- способ проверки готового физического результата.",
		"Все исполнители получают один и тот же неизменный договор.",
		"Уточнение договора может сделать только владелец объединения после нового указания пользователя или обнаруженного противоречия.",
		"Изменение нужно сообщить всем исполнителям до продолжения работы; нельзя незаметно менять смысл задачи для отдельного агента.",
	},
	"Coverage map": {
		"До параллельного запуска составить карту покрытия:",
		"- независимые зоны работы и ожидаемый результат каждой зоны;",
		"- владелец каждой зоны и точные файлы или пакеты, которыми он владеет;",
		"- зависимости и возможные пересечения;",
		"- критерий остановки для каждой зоны;",
		"- лимиты дорогих проверок и внешних источников;",
		"- один владелец объединения, который отвечает за итог.",
		"Количество агентов или найденных материалов не доказывает полноту.",
		"Полнота определяется закрытием всех зон карты и итоговой проверкой.",
	},
	"Ownership and conflicts": {
		"- Назначать одному файлу или пакету одного владельца на время изменения.",
		"- Независимые изменения в разных файлах или пакетах можно выполнять в общей рабочей папке.",
		"- Потенциально конфликтующие изменения выполнять в отдельных рабочих копиях Git (`worktree` - отдельная рабочая папка одной ветки).",
		"- Не разрешать нескольким агентам одновременно редактировать одну и ту же область.",
		"Если пересечение обнаружено после запуска, остановить одного владельца и переназначить границу.",
		"- Не откатывать и не переписывать чужие изменения.",
		"Считать незнакомые изменения пользовательскими, пока их происхождение не доказано.",
		"- Если задача объективно неделима, оставить одного исполнителя и независимого проверяющего; владелец объединения обязан кратко зафиксировать, почему дальнейшее безопасное разделение невозможно.",
	},
	"Integration owner": {
		"Только назначенный владелец объединения формирует итоговый результат.",
		"1. Сопоставить отчёты агентов с единым договором и картой покрытия.",
		"2. Проверить изменения непосредственно в рабочей папке; отчёт агента или успешная команда не являются доказательством результата.",
		"3. Устранить пересечения, противоречия и повторяющиеся реализации.",
		"4. Отделить проверенные факты от выводов и явно перечислить оставшуюся неопределённость.",
		"5. Проверить сквозное поведение через публичные границы компонентов, а не только отдельные внутренние функции.",
		"Нельзя считать готовностью статус агента, созданный файл, зелёный отдельный тест, локальную ссылку или успешный вызов инструмента.",
		"Нужен физический результат, соответствующий договору",
	},
	"Verification": {
		"Во время разработки выполнять быстрые профильные проверки только для изменённых компонентов и их непосредственных связей.",
		"Не запускать полный набор после каждого небольшого изменения.",
		"Перед выпуском версии обязателен полный набор проверок, охватывающий:",
		"- пользовательское поведение;",
		"- параллельную работу и порядок сообщений;",
		"- архитектурные границы пакетов;",
		"- безопасность и отсутствие секретов в коде, журналах и артефактах;",
		"- обновление и возврат к предыдущей рабочей версии;",
		"- поддерживаемые способы запуска и операционные системы;",
		"- настоящий пробный сценарий с внешними системами, если доступ и разрешение на него получены.",
		"Каждое исправление дефекта должно содержать проверку, которая воспроизводит наблюдаемое поведение до исправления и подтверждает результат после него.",
		"Если проверка невозможна, явно назвать непроверенную границу и не объявлять её готовой.",
	},
	"Safety and writes": {
		"Публикация, отправка сообщений, изменение удалённого репозитория, установка, перезапуск, удаление данных и другие видимые или труднообратимые действия разрешены только в явно согласованном объёме.",
		"До внешнего write:",
		"1. Показать точную bounded последовательность действий, точные цели и terminal criterion.",
		"2. Получить одно явное подтверждение на отдельное действие или bounded sequence. Подтверждение bounded sequence действует до указанного terminal criterion и включает необходимые commit, push и CI-fix iterations.",
		"3. Выполнить только этот согласованный scope.",
		"4. После каждого существенного внешнего write повторно прочитать целевое состояние и проверить фактический результат.",
		"Новые или расширенные targets требуют нового подтверждения.",
		"Deploy, изменения secrets, исходящие messages и любое destructive действие, включая destructive write к уже перечисленной цели, всегда требуют нового подтверждения, даже если они заранее перечислены.",
		"Неопределённый сетевой ответ не считать ни успехом, ни отказом: сначала безопасно перечитать состояние, затем решать вопрос о повторе.",
	},
}

var markdownLink = regexp.MustCompile(`!?\[[^\]]*\]\(([^)]+)\)`)

var ignoredLinkSchemes = []string{"http://", "https://", "mailto:", "tel:", "data:"}

var forbiddenDirectoryNames = map[string]struct{}{
	".avis": {}, "secrets": {}, "credentials": {}, "runtime": {}, "backup": {},
	"backups": {}, "archive": {}, "archives": {}, "logs": {},
}

var forbiddenExactNames = map[string]struct{}{
	".env": {}, "auth.json": {}, "credentials.json": {}, "state.json": {},
}

var forbiddenProductionBinaryNames = map[string]struct{}{
	"bria": {}, "bria-codex-adapter": {}, "bria-claude-adapter": {}, "bria-container-preflight": {},
}

var forbiddenSuffixes = []string{
	".key", ".pem", ".p12", ".token", ".session", ".session-journal",
	".db", ".sqlite", ".sqlite3", ".log", ".py",
}

func checkPolicy(root string) []string {
	policyPath := filepath.Join(root, "AGENTS.md")
	info, err := os.Stat(policyPath)
	if err != nil || !info.Mode().IsRegular() {
		return []string{"AGENTS.md is missing from the repository root"}
	}

	if code, err := commandExitCode(root, "git", "check-ignore", "--quiet", "--no-index", "AGENTS.md"); err != nil {
		return []string{"Git could not determine whether AGENTS.md is ignored"}
	} else if code == 0 {
		return []string{"AGENTS.md is excluded by a Git ignore rule"}
	} else if code != 1 {
		return []string{"Git could not determine whether AGENTS.md is ignored"}
	}

	headCode, headErr := commandExitCode(root, "git", "rev-parse", "--verify", "--quiet", "HEAD")
	if headErr != nil {
		return []string{"Git could not determine whether the repository has a commit"}
	}
	if headCode == 0 {
		trackedCode, trackedErr := commandExitCode(root, "git", "ls-files", "--error-unmatch", "AGENTS.md")
		if trackedErr != nil {
			return []string{"Git could not determine whether AGENTS.md is tracked"}
		}
		if trackedCode == 1 {
			return []string{"AGENTS.md must be tracked after the first commit"}
		}
		if trackedCode != 0 {
			return []string{"Git could not determine whether AGENTS.md is tracked"}
		}
	} else if headCode != 1 {
		return []string{"Git could not determine whether the repository has a commit"}
	}

	data, err := os.ReadFile(policyPath)
	if err != nil {
		return []string{fmt.Sprintf("read AGENTS.md: %v", err)}
	}
	text := string(data)
	if !regexp.MustCompile(`(?m)^#\s+\S`).MatchString(text) {
		return []string{"AGENTS.md must start with a meaningful level-one heading"}
	}

	sections := parseH2Sections(text)
	var problems []string
	for _, requirement := range requiredSections {
		body, exists := sections[requirement.name]
		if !exists {
			problems = append(problems, fmt.Sprintf(
				"AGENTS.md is missing required section '## %s'",
				requirement.name,
			))
			continue
		}
		if body == "" {
			problems = append(problems, fmt.Sprintf("section '## %s' is empty", requirement.name))
			continue
		}
		foldedBody := strings.ToLower(body)
		for _, alternatives := range requirement.conceptGroups {
			found := false
			for _, term := range alternatives {
				if strings.Contains(foldedBody, strings.ToLower(term)) {
					found = true
					break
				}
			}
			if !found {
				problems = append(problems, fmt.Sprintf(
					"section '## %s' does not state the required concept (%s)",
					requirement.name,
					strings.Join(alternatives, " / "),
				))
			}
		}
	}

	scalingItems := make(map[string]struct{})
	for _, line := range strings.Split(sections["Scaling rules"], "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "-") {
			scalingItems[strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))] = struct{}{}
		}
	}
	for _, invariant := range mandatoryInvariants {
		if _, exists := scalingItems[invariant]; !exists {
			problems = append(problems, "AGENTS.md is missing mandatory invariant: "+invariant)
		}
	}
	for section, clauses := range requiredPolicyClauses {
		body := normalizePolicyText(sections[section])
		for _, clause := range clauses {
			if !strings.Contains(body, normalizePolicyText(clause)) {
				problems = append(problems, fmt.Sprintf(
					"section '## %s' is missing required clause: %s",
					section,
					clause,
				))
			}
		}
	}
	return problems
}

func normalizePolicyText(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func parseH2Sections(text string) map[string]string {
	sections := make(map[string]string)
	current := ""
	lines := make(map[string][]string)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, "## ") {
			current = strings.TrimSpace(strings.TrimPrefix(line, "## "))
			if _, exists := lines[current]; !exists {
				lines[current] = nil
			}
			continue
		}
		if current != "" {
			lines[current] = append(lines[current], line)
		}
	}
	for name, body := range lines {
		sections[name] = strings.TrimSpace(strings.Join(body, "\n"))
	}
	return sections
}

func checkLinks(root string) []string {
	files, err := trackedFiles(root)
	if err != nil {
		return []string{err.Error()}
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return []string{fmt.Sprintf("resolve repository root: %v", err)}
	}
	var problems []string
	for _, relative := range files {
		if !strings.EqualFold(path.Ext(relative), ".md") {
			continue
		}
		document := filepath.Join(root, filepath.FromSlash(relative))
		data, err := os.ReadFile(document)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: read document: %v", relative, err))
			continue
		}
		for lineIndex, line := range strings.Split(string(data), "\n") {
			for _, match := range markdownLink.FindAllStringSubmatch(line, -1) {
				rawTarget := match[1]
				if hasAnyPrefix(rawTarget, ignoredLinkSchemes) || strings.HasPrefix(rawTarget, "#") {
					continue
				}
				target := normalizedLinkTarget(rawTarget)
				if target == "" {
					continue
				}
				var candidate string
				if strings.HasPrefix(target, "/") {
					candidate = filepath.Join(root, filepath.FromSlash(strings.TrimLeft(target, "/")))
				} else {
					candidate = filepath.Join(filepath.Dir(document), filepath.FromSlash(target))
				}
				resolved, resolveErr := resolveAllowMissing(candidate)
				if resolveErr != nil {
					problems = append(problems, fmt.Sprintf(
						"%s:%d: cannot resolve link target %s: %v",
						relative, lineIndex+1, rawTarget, resolveErr,
					))
					continue
				}
				if !pathWithin(resolvedRoot, resolved) {
					problems = append(problems, fmt.Sprintf(
						"%s:%d: link leaves repository: %s",
						relative, lineIndex+1, rawTarget,
					))
				} else if _, err := os.Stat(resolved); err != nil {
					problems = append(problems, fmt.Sprintf(
						"%s:%d: missing link target: %s",
						relative, lineIndex+1, rawTarget,
					))
				}
			}
		}
	}
	return problems
}

func normalizedLinkTarget(rawTarget string) string {
	target := strings.TrimSpace(rawTarget)
	if strings.HasPrefix(target, "<") {
		if closing := strings.Index(target, ">"); closing >= 0 {
			target = target[1:closing]
		}
	} else if space := strings.Index(target, " "); space >= 0 {
		target = target[:space]
	}
	if unescaped, err := url.PathUnescape(target); err == nil {
		target = unescaped
	}
	if fragment := strings.Index(target, "#"); fragment >= 0 {
		target = target[:fragment]
	}
	if query := strings.Index(target, "?"); query >= 0 {
		target = target[:query]
	}
	return target
}

func resolveAllowMissing(candidate string) (string, error) {
	absolute, err := filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	current := filepath.Clean(absolute)
	var missing []string
	for {
		if _, err := os.Stat(current); err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for index := len(missing) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missing[index])
			}
			return resolved, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return current, nil
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func checkFilenames(root string) []string {
	files, err := trackedFiles(root)
	if err != nil {
		return []string{err.Error()}
	}
	var problems []string
	for _, filename := range files {
		parts := strings.Split(filename, "/")
		// Runtime directories are repository-root locations. A production
		// package such as internal/backup is source code, not generated backup
		// data, and must remain trackable.
		if len(parts) > 1 {
			if _, forbidden := forbiddenDirectoryNames[strings.ToLower(parts[0])]; forbidden {
				problems = append(problems, "tracked runtime or private directory: "+filename)
			}
		}
		name := strings.ToLower(parts[len(parts)-1])
		if _, forbidden := forbiddenProductionBinaryNames[name]; forbidden && (len(parts) == 1 || strings.ToLower(parts[0]) != "dist") {
			problems = append(problems, "tracked production binary outside dist: "+filename)
		}
		_, exactForbidden := forbiddenExactNames[name]
		if exactForbidden || strings.HasPrefix(name, ".env.") || hasAnySuffix(name, forbiddenSuffixes) {
			problems = append(problems, "tracked secret or runtime filename: "+filename)
		}
	}
	return append(problems, checkSecretContents(root)...)
}

var mandatoryReleaseEvidence = []string{
	"binary_identity",
	"platform_macos",
	"platform_linux",
	"platform_wsl",
	"platform_docker_coordinator",
	"platform_docker_executor",
	"telegram_codex_claude_e2e",
	"concurrency_and_recovery",
	"update_and_forced_rollback",
	"backup_restore",
	"architecture_and_secret_scan",
}

const (
	maxSecretScanFileBytes    = 128 << 20
	maxSecretScanArchiveBytes = 512 << 20
	maxEvidenceManifestBytes  = 1 << 20
	maxEvidenceReceiptBytes   = 32 << 20
)

var secretContentPatterns = []struct {
	name    string
	pattern *regexp.Regexp
}{
	{name: "private key", pattern: regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----`)},
	{name: "AWS access key", pattern: regexp.MustCompile(`AKIA[0-9A-Z]{16}`)},
	{name: "GitHub token", pattern: regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{36,}`)},
	{name: "provider API key", pattern: regexp.MustCompile(`sk-(?:ant-)?[A-Za-z0-9_-]{20,}`)},
	{name: "Telegram bot token", pattern: regexp.MustCompile(`[0-9]{8,12}:[A-Za-z0-9_-]{30,}`)},
	{name: "literal bearer credential", pattern: regexp.MustCompile(`(?i)bearer[ \t]+[A-Za-z0-9._~+/-]{24,}`)},
}

func checkSecretContents(root string) []string {
	tracked, err := trackedFiles(root)
	if err != nil {
		return []string{err.Error()}
	}
	candidates := make(map[string]bool, len(tracked))
	for _, relative := range tracked {
		if shouldScanSourceContent(relative) {
			candidates[filepath.Join(root, filepath.FromSlash(relative))] = false
		}
	}
	for _, directory := range releaseArtifactDirectories(root) {
		info, err := os.Stat(directory)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.IsDir() {
			return []string{"inspect release artifact directory"}
		}
		walkErr := filepath.WalkDir(directory, func(candidate string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type().IsRegular() {
				candidates[candidate] = true
			}
			return nil
		})
		if walkErr != nil {
			return []string{"walk release artifacts"}
		}
	}
	paths := make([]string, 0, len(candidates))
	for candidate := range candidates {
		paths = append(paths, candidate)
	}
	sort.Strings(paths)
	var problems []string
	for _, candidate := range paths {
		// Deliberate credential-shaped regression fixtures are permitted in Go
		// test sources. They are never compiled into release artifacts, which are
		// scanned without this exclusion.
		if !candidates[candidate] && strings.HasSuffix(candidate, "_test.go") {
			continue
		}
		info, err := os.Lstat(candidate)
		if err != nil {
			problems = append(problems, "inspect secret-scan candidate: "+displayPath(root, candidate))
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if strings.HasSuffix(strings.ToLower(candidate), ".tar.gz") {
			problems = append(problems, scanReleaseArchive(root, candidate)...)
			continue
		}
		if info.Size() > maxSecretScanFileBytes {
			problems = append(problems, "secret scan candidate exceeds size limit: "+displayPath(root, candidate))
			continue
		}
		contents, err := os.ReadFile(candidate)
		if err != nil {
			problems = append(problems, "read secret-scan candidate: "+displayPath(root, candidate))
			continue
		}
		if secret := findSecret(contents); secret != "" {
			kind := "probable secret content"
			if candidates[candidate] {
				kind += " in release artifact"
			}
			problems = append(problems, fmt.Sprintf("%s: %s (%s)", kind, displayPath(root, candidate), secret))
		}
	}
	return problems
}

func shouldScanSourceContent(relative string) bool {
	base := filepath.Base(relative)
	if base == "Dockerfile" || base == "Makefile" {
		return true
	}
	switch strings.ToLower(filepath.Ext(base)) {
	case ".go", ".sh", ".json", ".yaml", ".yml", ".md", ".mod", ".sum", ".txt", ".tmpl":
		return true
	default:
		return false
	}
}

func releaseArtifactDirectories(root string) []string {
	result := []string{filepath.Join(root, "bin"), filepath.Join(root, "build"), filepath.Join(root, "dist")}
	if configured := os.Getenv("DIST_DIR"); configured != "" && filepath.IsAbs(configured) {
		result = append(result, configured)
	}
	return sortedUnique(result)
}

func scanReleaseArchive(root, archivePath string) []string {
	file, err := os.Open(archivePath)
	if err != nil {
		return []string{"open release archive for secret scan: " + displayPath(root, archivePath)}
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return []string{"unreadable release archive during secret scan: " + displayPath(root, archivePath)}
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	remaining := int64(maxSecretScanArchiveBytes)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return []string{"unreadable release archive during secret scan: " + displayPath(root, archivePath)}
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			continue
		}
		if header.Size < 0 || header.Size > maxSecretScanFileBytes || header.Size > remaining {
			return []string{"release archive exceeds secret scan size limit: " + displayPath(root, archivePath)}
		}
		contents, err := io.ReadAll(io.LimitReader(tarReader, header.Size+1))
		if err != nil || int64(len(contents)) != header.Size {
			return []string{"unreadable release archive member during secret scan: " + displayPath(root, archivePath)}
		}
		remaining -= header.Size
		if secret := findSecret(contents); secret != "" {
			return []string{fmt.Sprintf(
				"probable secret content in release archive: %s!%s (%s)",
				displayPath(root, archivePath), header.Name, secret,
			)}
		}
	}
}

func findSecret(contents []byte) string {
	for _, candidate := range secretContentPatterns {
		if candidate.pattern.FindIndex(contents) != nil {
			return candidate.name
		}
	}
	return ""
}

func displayPath(root, candidate string) string {
	if relative, err := filepath.Rel(root, candidate); err == nil && pathWithin(root, candidate) {
		return filepath.ToSlash(relative)
	}
	return filepath.Clean(candidate)
}

type releaseEvidenceManifest struct {
	SchemaVersion         int                       `json:"schema_version"`
	ReleaseVersion        string                    `json:"release_version"`
	Revision              string                    `json:"revision"`
	ProducerRunID         string                    `json:"producer_run_id"`
	GeneratedAt           string                    `json:"generated_at"`
	ReleaseManifestSHA256 string                    `json:"release_manifest_sha256"`
	Artifacts             []releaseEvidenceArtifact `json:"artifacts"`
	Receipts              []releaseEvidenceReceipt  `json:"receipts"`
}

type releaseEvidenceArtifact struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

type releaseEvidenceReceipt struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	Environment  string `json:"environment"`
	Scenario     string `json:"scenario"`
	ObservedAt   string `json:"observed_at"`
	EvidenceFile string `json:"evidence_file"`
	SHA256       string `json:"sha256"`
}

func checkReleaseEvidence(root string) []string {
	manifestPath := os.Getenv("BRIA_RELEASE_EVIDENCE_MANIFEST")
	if manifestPath == "" || !filepath.IsAbs(manifestPath) {
		return []string{"BRIA_RELEASE_EVIDENCE_MANIFEST must name an absolute evidence manifest"}
	}
	expectedVersion := os.Getenv("VERSION")
	expectedRevision := os.Getenv("REVISION")
	releaseManifestPath := os.Getenv("BRIA_RELEASE_MANIFEST")
	if releaseManifestPath == "" || !filepath.IsAbs(releaseManifestPath) {
		return []string{"BRIA_RELEASE_MANIFEST must name an absolute signed release manifest"}
	}
	problems := validateBoundReleaseEvidence(manifestPath, expectedVersion, expectedRevision, releaseManifestPath)
	packages, err := goListPackages(root)
	if err != nil {
		problems = append(problems, err.Error())
	} else {
		problems = append(problems, externalOnlyReleaseBlockers(packages)...)
	}
	return append(problems, checkSecretContents(root)...)
}

func externalOnlyReleaseBlockers(packages []packageInfo) []string {
	var blockers []string
	for _, pkg := range packages {
		policy, registered := packagePolicies[pkg.RelativePath]
		if pkg.HasProduction && registered && policy.externalOnlyEvidence != "" {
			blockers = append(blockers, fmt.Sprintf(
				"external-only product package blocks release: %s (required evidence category %s)",
				pkg.RelativePath, policy.externalOnlyEvidence,
			))
		}
	}
	sort.Strings(blockers)
	return blockers
}

func validateReleaseEvidence(manifestPath, expectedVersion string) []string {
	return validateBoundReleaseEvidence(
		manifestPath,
		expectedVersion,
		"revision-1",
		filepath.Join(filepath.Dir(manifestPath), "release-manifest.json"),
	)
}

func validateBoundReleaseEvidence(manifestPath, expectedVersion, expectedRevision, releaseManifestPath string) []string {
	if !validReleaseVersion(expectedVersion) {
		return []string{"release evidence requires a non-dev VERSION"}
	}
	if !validSourceIdentity(expectedRevision) {
		return []string{"release evidence requires a valid REVISION"}
	}
	info, err := os.Lstat(manifestPath)
	if errors.Is(err, os.ErrNotExist) {
		return []string{"release evidence manifest is unavailable"}
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxEvidenceManifestBytes {
		return []string{"release evidence manifest is not a bounded regular file"}
	}
	file, err := os.Open(manifestPath)
	if err != nil {
		return []string{"open release evidence manifest"}
	}
	decoder := json.NewDecoder(io.LimitReader(file, maxEvidenceManifestBytes+1))
	decoder.DisallowUnknownFields()
	var manifest releaseEvidenceManifest
	decodeErr := decoder.Decode(&manifest)
	var trailing any
	trailingErr := decoder.Decode(&trailing)
	closeErr := file.Close()
	if decodeErr != nil || !errors.Is(trailingErr, io.EOF) || closeErr != nil {
		return []string{"release evidence manifest is invalid JSON"}
	}
	if manifest.SchemaVersion != 1 || manifest.ReleaseVersion != expectedVersion {
		return []string{"release evidence manifest identity mismatch"}
	}
	var problems []string
	if manifest.Revision != expectedRevision {
		problems = append(problems, "release evidence source revision mismatch")
	}
	if !validProducerRunID(manifest.ProducerRunID) {
		problems = append(problems, "release evidence producer run identity is invalid")
	}
	generatedAt, generatedErr := parseFreshEvidenceTime(manifest.GeneratedAt, time.Now())
	if generatedErr != nil {
		problems = append(problems, "release evidence manifest is stale or has invalid generated_at")
	}
	expectedArtifacts, releaseManifestDigest, artifactErr := releaseArtifactDigests(releaseManifestPath, expectedVersion)
	if artifactErr != nil {
		problems = append(problems, artifactErr.Error())
	} else {
		if manifest.ReleaseManifestSHA256 != releaseManifestDigest {
			problems = append(problems, "release manifest digest mismatch")
		}
		problems = append(problems, compareEvidenceArtifacts(manifest.Artifacts, expectedArtifacts)...)
	}
	manifestDirectory := filepath.Dir(manifestPath)
	seenIDs := make(map[string]struct{}, len(manifest.Receipts))
	seenFiles := make(map[string]struct{}, len(manifest.Receipts))
	for _, receipt := range manifest.Receipts {
		if _, exists := seenIDs[receipt.ID]; exists || receipt.ID == "" {
			problems = append(problems, "duplicate or empty release evidence id")
			continue
		}
		seenIDs[receipt.ID] = struct{}{}
		if receipt.Status != "verified" || strings.TrimSpace(receipt.Environment) == "" || strings.TrimSpace(receipt.Scenario) == "" {
			problems = append(problems, "release evidence is not verified: "+receipt.ID)
		}
		observedAt, err := parseFreshEvidenceTime(receipt.ObservedAt, time.Now())
		if err != nil || (generatedErr == nil && observedAt.After(generatedAt.Add(5*time.Minute))) {
			problems = append(problems, "release evidence is stale or has invalid observed_at: "+receipt.ID)
		}
		candidate, pathErr := resolveEvidenceFile(manifestDirectory, receipt.EvidenceFile)
		if pathErr != nil {
			problems = append(problems, "release evidence file is invalid: "+receipt.ID)
			continue
		}
		if _, exists := seenFiles[candidate]; exists {
			problems = append(problems, "release evidence receipts must use distinct files: "+receipt.ID)
			continue
		}
		seenFiles[candidate] = struct{}{}
		contents, readErr := readEvidenceReceipt(candidate)
		if readErr != nil {
			problems = append(problems, "release evidence receipt is unavailable: "+receipt.ID)
			continue
		}
		digest := sha256.Sum256(contents)
		if receipt.SHA256 != fmt.Sprintf("%x", digest) {
			problems = append(problems, "evidence receipt digest mismatch: "+receipt.ID)
		}
		if secret := findSecret(contents); secret != "" {
			problems = append(problems, "probable secret content in evidence receipt: "+receipt.ID+" ("+secret+")")
		}
	}
	for _, id := range mandatoryReleaseEvidence {
		if _, exists := seenIDs[id]; !exists {
			problems = append(problems, "missing mandatory release evidence: "+id)
		}
	}
	return problems
}

func validReleaseVersion(version string) bool {
	return version != "" && version != "dev" && regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._+-]*$`).MatchString(version) && !strings.Contains(version, "..")
}

func validSourceIdentity(value string) bool {
	return value != "" && len(value) <= 160 && regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._+-]*$`).MatchString(value)
}

func validProducerRunID(value string) bool {
	return value != "" && len(value) <= 160 && regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._:/+-]*$`).MatchString(value)
}

func parseFreshEvidenceTime(value string, now time.Time) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil || parsed.Before(now.Add(-7*24*time.Hour)) || parsed.After(now.Add(5*time.Minute)) {
		return time.Time{}, errors.New("stale evidence time")
	}
	return parsed, nil
}

func compareEvidenceArtifacts(declared []releaseEvidenceArtifact, expected map[string]string) []string {
	seen := make(map[string]struct{}, len(declared))
	var problems []string
	for _, artifact := range declared {
		if _, duplicate := seen[artifact.Name]; duplicate || artifact.Name == "" {
			problems = append(problems, "duplicate or empty release evidence artifact")
			continue
		}
		seen[artifact.Name] = struct{}{}
		digest, exists := expected[artifact.Name]
		if !exists {
			problems = append(problems, "unexpected release evidence artifact: "+artifact.Name)
			continue
		}
		if artifact.SHA256 != digest {
			problems = append(problems, "release artifact digest mismatch: "+artifact.Name)
		}
	}
	for name := range expected {
		if _, exists := seen[name]; !exists {
			problems = append(problems, "missing release evidence artifact: "+name)
		}
	}
	return problems
}

func releaseArtifactDigests(releaseManifestPath, version string) (map[string]string, string, error) {
	if !filepath.IsAbs(releaseManifestPath) || filepath.Base(releaseManifestPath) != "release-manifest.json" {
		return nil, "", errors.New("signed release manifest path is invalid")
	}
	manifestContents, err := readBoundedRegular(releaseManifestPath, maxEvidenceReceiptBytes)
	if err != nil {
		return nil, "", errors.New("signed release manifest is unavailable")
	}
	manifestDigest := sha256Hex(manifestContents)
	result := map[string]string{"release-manifest.json": manifestDigest}
	directory := filepath.Dir(releaseManifestPath)
	expectedArchives := []string{
		"bria_" + version + "_darwin_amd64.tar.gz",
		"bria_" + version + "_darwin_arm64.tar.gz",
		"bria_" + version + "_linux_amd64.tar.gz",
		"bria_" + version + "_linux_arm64.tar.gz",
	}
	expectedSet := make(map[string]struct{}, len(expectedArchives))
	for _, name := range expectedArchives {
		expectedSet[name] = struct{}{}
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, "", errors.New("read signed release directory")
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tar.gz") {
			if _, expected := expectedSet[entry.Name()]; !expected {
				return nil, "", errors.New("signed release directory has unexpected archive")
			}
		}
	}
	for _, name := range expectedArchives {
		archivePath := filepath.Join(directory, name)
		archiveContents, err := readBoundedRegular(archivePath, maxSecretScanFileBytes)
		if err != nil {
			return nil, "", errors.New("signed release archive is unavailable: " + name)
		}
		result[name] = sha256Hex(archiveContents)
		binaryDigests, err := releaseBinaryDigests(archivePath)
		if err != nil {
			return nil, "", fmt.Errorf("inspect signed release archive %s: %w", name, err)
		}
		for member, digest := range binaryDigests {
			result[name+"!"+member] = digest
		}
	}
	return result, manifestDigest, nil
}

func releaseBinaryDigests(archivePath string) (map[string]string, error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return nil, err
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	required := map[string]struct{}{"bria": {}, "bria-codex-adapter": {}, "bria-claude-adapter": {}}
	result := make(map[string]string, len(required))
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		base := path.Base(header.Name)
		if _, wanted := required[base]; !wanted || (header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA) {
			continue
		}
		if _, duplicate := result[base]; duplicate || header.Size < 0 || header.Size > maxSecretScanFileBytes {
			return nil, errors.New("duplicate or invalid release binary")
		}
		hash := sha256.New()
		written, err := io.CopyN(hash, tarReader, header.Size)
		if err != nil || written != header.Size {
			return nil, errors.New("read release binary")
		}
		result[base] = fmt.Sprintf("%x", hash.Sum(nil))
	}
	if len(result) != len(required) {
		return nil, errors.New("release archive is missing executable trio")
	}
	return result, nil
}

func readBoundedRegular(candidate string, limit int64) ([]byte, error) {
	info, err := os.Lstat(candidate)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > limit {
		return nil, errors.New("not a bounded regular file")
	}
	return os.ReadFile(candidate)
}

func sha256Hex(contents []byte) string {
	digest := sha256.Sum256(contents)
	return fmt.Sprintf("%x", digest)
}

func resolveEvidenceFile(directory, relative string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) || filepath.Clean(relative) == "." {
		return "", errors.New("invalid evidence path")
	}
	candidate := filepath.Join(directory, filepath.FromSlash(relative))
	info, err := os.Lstat(candidate)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("evidence path is not a regular file")
	}
	resolvedDirectory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil || !pathWithin(resolvedDirectory, resolved) {
		return "", errors.New("evidence path escapes manifest directory")
	}
	return resolved, nil
}

func readEvidenceReceipt(candidate string) ([]byte, error) {
	info, err := os.Lstat(candidate)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxEvidenceReceiptBytes {
		return nil, errors.New("invalid evidence receipt")
	}
	return os.ReadFile(candidate)
}

func trackedFiles(root string) ([]string, error) {
	command := exec.Command("git", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("list repository files: %w", err)
	}
	items := bytes.Split(output, []byte{0})
	files := make([]string, 0, len(items))
	for _, item := range items {
		if len(item) != 0 {
			files = append(files, string(item))
		}
	}
	sort.Strings(files)
	return files, nil
}

type packageInfo struct {
	ImportPath      string
	RelativePath    string
	Imports         []string
	TestImports     []string
	XTestImports    []string
	HasProduction   bool
	ProductionLines int
}

type importOrigin uint8

const (
	importProduction importOrigin = iota
	importInternalTest
	importExternalTest
)

type packageImport struct {
	path   string
	origin importOrigin
}

var forbiddenPackageDirectories = map[string]struct{}{
	"common": {}, "helpers": {}, "interfaces": {}, "manager": {},
	"types": {}, "utils": {},
}

var telegramAppForbiddenImports = []string{
	"internal/telegram", "internal/storage", "internal/sessionruntime",
	"internal/executor", "internal/provider",
}

var sessionIDForbiddenImports = []string{
	"internal/telegram", "internal/telegramapp", "internal/telegramui",
	"internal/storage", "internal/sessionruntime", "internal/executor",
	"internal/provider", "cmd",
}

var configAllowedImports = []string{"internal/domain"}

var workdirAllowedImports = []string{"internal/app", "internal/domain"}

var telegramUIAllowedImports = []string{"internal/app", "internal/domain"}

var providerAdapterAllowedImports = []string{
	"internal/app", "internal/domain", "internal/processgroup",
}

var processInfrastructureRoots = []string{
	"internal/processenv", "internal/processgroup", "internal/instancelock",
}

var runtimeFactoryAllowedImports = []string{
	"internal/app", "internal/config", "internal/domain", "internal/processenv", "internal/sessionruntime",
}

var sessionRuntimeAllowedImports = []string{
	"internal/app", "internal/domain", "internal/processgroup", "internal/runtimeprotocol",
}

var telegramControllerAllowedImports = []string{
	"internal/app", "internal/coordinator", "internal/domain", "internal/sessionruntime", "internal/settingsport", "internal/telegramsettings", "internal/turnprocessing",
}

var telegramBridgeAllowedImports = []string{
	"internal/callbacktoken", "internal/coordinator", "internal/telegram", "internal/telegramrecovery", "internal/telegramui",
}

var telegramNotifyAllowedImports = []string{
	"internal/domain", "internal/telegram", "internal/telegramcontroller", "internal/telegramui",
}

type packagePolicy struct {
	responsibility       string
	allowedImports       []string
	maxProductionLines   int
	compositionRoot      bool
	externalOnlyEvidence string
}

// packagePolicies is the architecture boundary registry. Internal production
// packages are default-deny: adding a package requires naming its single
// responsibility and its allowed local dependencies here. Most packages have
// a 700-line ceiling; larger explicit ceilings cap already-landed adapter and
// persistence debt instead of making today's repository permanently red.
// externalOnlyEvidence is an honest temporary classification for product code
// which is not yet reachable from a supported command. It does not relax
// dependencies and always blocks the strict release gate, regardless of any
// supplied receipt. It must be removed as soon as composition becomes real.
var packagePolicies = map[string]packagePolicy{
	"cmd/bria": {
		// This budget covers only the current P1/P2 composition. New feature
		// adapters belong in bounded internal packages, not in this root.
		responsibility: "compose the coordinator process",
		allowedImports: []string{
			"internal/app", "internal/config", "internal/coordinator", "internal/domain", "internal/instancelock", "internal/sessionruntime", "internal/settings", "internal/singlemachinecomposition", "internal/storage", "internal/telegram", "internal/telegrambridge", "internal/telegramnotify",
		},
		maxProductionLines: 1700,
		compositionRoot:    true,
	},
	"internal/artifactcomposition": {
		responsibility:     "route terminal observations into durable artifact delivery",
		allowedImports:     []string{"internal/artifactproduction", "internal/turnprocessing"},
		maxProductionLines: 150,
	},
	"cmd/bria-claude-adapter": {
		responsibility:     "compose the Claude adapter process",
		allowedImports:     []string{"internal/processgroup", "internal/provider/claude"},
		maxProductionLines: 400,
		compositionRoot:    true,
	},
	"cmd/bria-codex-adapter": {
		responsibility:     "compose the Codex adapter process",
		allowedImports:     []string{"internal/provider/codex"},
		maxProductionLines: 200,
		compositionRoot:    true,
	},
	"internal/app": {
		responsibility:     "implement provider-independent session use cases",
		allowedImports:     []string{"internal/domain"},
		maxProductionLines: 900,
	},
	"internal/artifactproduction": {
		responsibility:     "compose durable final artifact delivery with content integrity, exact attempt receipts, and fenced manual-retry recovery",
		allowedImports:     []string{"internal/artifactdelivery", "internal/files", "internal/telegram"},
		maxProductionLines: 1000,
	},
	"internal/artifactretrycomposition": {
		responsibility: "own the durable manual decision between an unconfirmed artifact delivery and its explicit retry callback",
		allowedImports: []string{
			"internal/artifactdelivery", "internal/artifactproduction", "internal/domain", "internal/telegrambridge", "internal/telegramflow", "internal/telegrampipeline", "internal/telegramui", "internal/turnprocessing",
		},
		maxProductionLines: 700,
	},
	"internal/artifactruntimecomposition": {
		responsibility:     "assemble the opt-in artifact final and manual retry path from explicit P4 configuration only",
		allowedImports:     []string{"internal/artifactcomposition", "internal/artifactproduction", "internal/artifactretrycomposition", "internal/config", "internal/secretfile", "internal/telegram", "internal/telegrambridge", "internal/telegramflow", "internal/turnprocessing"},
		maxProductionLines: 200,
	},
	"internal/archiveimport": {
		responsibility:     "validate and merge externally discovered archived sessions",
		allowedImports:     []string{"internal/domain"},
		maxProductionLines: 200,
	},
	"internal/artifactdelivery": {
		responsibility:     "deliver validated local artifacts without duplicate sends",
		allowedImports:     []string{"internal/files"},
		maxProductionLines: 700,
	},
	"internal/authflow": {
		// Durable secret deletion and restart tombstones belong to the same
		// bounded authorization lifecycle; provider adapters remain separate.
		// Further growth must split the store instead of raising this cap.
		responsibility:     "perform local provider authorization without exposing credentials",
		maxProductionLines: 1500,
	},
	"internal/authcomposition": {
		responsibility: "connect Telegram authorization to durable provider authenticators on one local computer",
		allowedImports: []string{
			"internal/authflow", "internal/domain", "internal/provider/claude", "internal/provider/codex",
			"internal/telegram", "internal/telegramcontroller",
		},
		maxProductionLines: 300,
	},
	"internal/backup": {
		responsibility:       "create and verify bounded local backups",
		maxProductionLines:   700,
		externalOnlyEvidence: "backup_restore",
	},
	"internal/backupflow": {
		responsibility:       "orchestrate verified backup creation and restore",
		allowedImports:       []string{"internal/backup"},
		maxProductionLines:   900,
		externalOnlyEvidence: "backup_restore",
	},
	"internal/backupcomposition": {
		responsibility:       "compose manual verified backup and restore operations",
		allowedImports:       []string{"internal/backupflow", "internal/backupruntime", "internal/backupsource", "internal/domain"},
		maxProductionLines:   600,
		externalOnlyEvidence: "backup_restore",
	},
	"internal/backupsource": {
		responsibility: "export and restore typed semantic state for verified backups",
		allowedImports: []string{
			"internal/backupruntime", "internal/computer", "internal/domain", "internal/messagejournal", "internal/settings",
		},
		maxProductionLines:   700,
		externalOnlyEvidence: "backup_restore",
	},
	"internal/backupruntime": {
		responsibility:       "compose semantic state export with verified backup operations",
		allowedImports:       []string{"internal/backup", "internal/backupflow"},
		maxProductionLines:   1000,
		externalOnlyEvidence: "backup_restore",
	},
	"internal/callbacktoken": {
		responsibility:     "sign and verify callback capability tokens",
		maxProductionLines: 700,
	},
	"internal/claudestore": {
		responsibility:     "read and verify Claude provider-owned transcript records",
		maxProductionLines: 600,
	},
	"internal/computer": {
		responsibility:       "model computer identity and coordinator fencing",
		allowedImports:       []string{"internal/domain"},
		maxProductionLines:   700,
		externalOnlyEvidence: "concurrency_and_recovery",
	},
	"internal/containerpreflight": {
		responsibility:       "verify immutable mounted provider artifacts before a Docker role starts",
		allowedImports:       []string{"internal/config"},
		maxProductionLines:   450,
		externalOnlyEvidence: "platform_docker_executor",
	},
	"internal/config": {
		responsibility:     "load and validate process configuration",
		allowedImports:     []string{"internal/domain"},
		maxProductionLines: 2000,
	},
	"internal/durablecomposition": {
		responsibility: "compose durable message custody and accepted-turn reconciliation",
		allowedImports: []string{
			"internal/domain", "internal/durableflow", "internal/messagejournal", "internal/sessionsupervisor", "internal/telegramcontroller", "internal/telegramnotify",
		},
		maxProductionLines: 500,
	},
	"internal/coordinator": {
		responsibility:     "serialize coordinator commands and durable effects",
		allowedImports:     []string{"internal/coordinator/recoverycontrol"},
		maxProductionLines: 800,
	},
	"internal/coordinatorbundle": {
		responsibility: "define the complete credential-free state moved during coordinator handoff",
		allowedImports: []string{
			"internal/computer", "internal/coordinator", "internal/domain", "internal/messagejournal",
			"internal/settings", "internal/telegramflow", "internal/telegrampipeline", "internal/telegramstate", "internal/telegramui",
		},
		maxProductionLines:   300,
		externalOnlyEvidence: "concurrency_and_recovery",
	},
	"internal/coordinatorstate": {
		responsibility:       "persist atomic coordinator transfer snapshots",
		allowedImports:       []string{"internal/coordinatorbundle", "internal/coordinatortransfer"},
		maxProductionLines:   700,
		externalOnlyEvidence: "concurrency_and_recovery",
	},
	"internal/coordinatortransfer": {
		responsibility: "perform owner-approved manual coordinator handoff",
		allowedImports: []string{
			"internal/computer", "internal/coordinatorbundle", "internal/domain", "internal/nodelink",
		},
		maxProductionLines:   600,
		externalOnlyEvidence: "concurrency_and_recovery",
	},
	"internal/domain": {
		responsibility:     "define dependency-free product state and rules",
		maxProductionLines: 700,
	},
	"internal/durableflow": {
		responsibility:     "process durable ordered message journal work",
		allowedImports:     []string{"internal/messagejournal"},
		maxProductionLines: 700,
	},
	"internal/executor": {
		responsibility:       "execute coordinator commands on an owning computer",
		allowedImports:       []string{"internal/computer", "internal/domain", "internal/nodelink"},
		maxProductionLines:   700,
		externalOnlyEvidence: "concurrency_and_recovery",
	},
	"internal/files": {
		responsibility:     "stage and validate outbound local files",
		maxProductionLines: 700,
	},
	"internal/mediaflow": {
		// This bounded adapter budget includes the complete inbound attachment
		// custody validation; downstream production adapters stay separate.
		responsibility: "orchestrate bounded inbound media preparation",
		allowedImports: []string{
			"internal/files", "internal/speech", "internal/telegram", "internal/telegramcontroller",
		},
		maxProductionLines: 350,
	},
	"internal/mediaproduction": {
		responsibility:     "compose production media adapters and durable photo custody",
		allowedImports:     []string{"internal/files", "internal/mediaflow", "internal/speech/parakeet"},
		maxProductionLines: 800,
	},
	"internal/instancelock": {
		responsibility:     "enforce one local Bria process per state root",
		maxProductionLines: 700,
	},
	"internal/inputcomposition": {
		responsibility:     "connect production media preparation to structured durable attachment custody",
		allowedImports:     []string{"internal/mediaproduction", "internal/turnprocessing"},
		maxProductionLines: 150,
	},
	"internal/interactionflow": {
		responsibility: "persist and route provider interactions through signed Telegram delivery",
		allowedImports: []string{
			"internal/coordinator", "internal/domain", "internal/interactionstore", "internal/runtimeprotocol", "internal/sessionruntime",
			"internal/telegrambridge", "internal/telegramcontroller", "internal/telegramflow",
			"internal/telegrampipeline", "internal/telegramui",
		},
		maxProductionLines: 1200,
	},
	"internal/interactionstore": {
		responsibility:     "persist at-most-once provider interaction operations",
		allowedImports:     []string{"internal/domain", "internal/interactionsourcestore", "internal/runtimeprotocol"},
		maxProductionLines: 700,
	},
	"internal/interactionsourcestore": {
		responsibility:     "persist bounded content-free Telegram source-consumption tombstones",
		maxProductionLines: 400,
	},
	"internal/interactioncomposition": {
		responsibility: "compose durable provider interactions with Telegram delivery adapters",
		allowedImports: []string{
			"internal/interactionflow", "internal/telegram", "internal/telegrambridge", "internal/telegramflow", "internal/telegrampipeline",
		},
		maxProductionLines: 200,
	},
	"internal/messagejournal": {
		// This budget includes the versioned attachment custody schema and its
		// ordered input/output journal; execution remains in durableflow.
		responsibility:     "persist ordered inbound and outbound messages",
		maxProductionLines: 1400,
	},
	"internal/multinodecomposition": {
		responsibility:       "compose durable multi-computer coordinator roles and manual cutover",
		allowedImports:       []string{"internal/coordinatortransfer", "internal/nodebootstrap", "internal/nodelink"},
		maxProductionLines:   700,
		externalOnlyEvidence: "concurrency_and_recovery",
	},
	"internal/nodelink": {
		responsibility:       "pair computers and exchange idempotent node messages",
		allowedImports:       []string{"internal/computer", "internal/domain"},
		maxProductionLines:   1600,
		externalOnlyEvidence: "concurrency_and_recovery",
	},
	"internal/nodebootstrap": {
		responsibility:       "bootstrap pairing between a computer and coordinator",
		allowedImports:       []string{"internal/computer", "internal/nodelink"},
		maxProductionLines:   500,
		externalOnlyEvidence: "concurrency_and_recovery",
	},
	"internal/processenv": {
		responsibility:     "construct minimal child-process environments",
		maxProductionLines: 700,
	},
	"internal/processgroup": {
		responsibility:     "control provider process trees across platforms",
		maxProductionLines: 700,
	},
	"internal/provider/claude": {
		responsibility: "adapt the Claude CLI protocol",
		allowedImports: []string{
			"internal/authflow", "internal/domain", "internal/runtimeprotocol", "internal/sessionsupervisor",
		},
		maxProductionLines: 2500,
	},
	"internal/provider/codex": {
		responsibility: "adapt the Codex app-server protocol",
		allowedImports: []string{
			"internal/authflow", "internal/domain", "internal/processgroup", "internal/runtimeprotocol",
			"internal/sessiondiscovery", "internal/sessionsupervisor",
		},
		maxProductionLines: 3050,
	},
	"internal/recoverycomposition": {
		responsibility:     "adapt provider-neutral accepted-turn recovery reads to lifecycle supervision",
		allowedImports:     []string{"internal/domain", "internal/sessionruntime", "internal/sessionsupervisor"},
		maxProductionLines: 100,
	},
	"internal/recoveryruntime": {
		responsibility:     "run bounded provider adapters as read-only accepted-turn history readers",
		allowedImports:     []string{"internal/claudestore", "internal/domain", "internal/processgroup", "internal/runtimeprotocol", "internal/sessionruntime"},
		maxProductionLines: 400,
	},
	"internal/coordinator/recoverycontrol": {
		responsibility:     "bind an unknown operation to its separate signed recovery prompt",
		maxProductionLines: 50,
	},
	"internal/telegramrecovery/statusrecovery": {
		responsibility:     "define identity for resolving one exact unknown Telegram status write",
		allowedImports:     []string{"internal/domain", "internal/telegramstate", "internal/telegramui"},
		maxProductionLines: 100,
	},
	"internal/telegramrecoverycomposition": {
		responsibility: "resolve signed owner recovery clicks against exact durable Telegram operations and request a fresh projection",
		allowedImports: []string{
			"internal/coordinator", "internal/telegrambridge", "internal/telegramflow", "internal/telegramrecovery/statusrecovery",
			"internal/telegrampipeline", "internal/telegramstate", "internal/telegramui",
		},
		maxProductionLines: 400,
	},
	"internal/telegramruntimecomposition": {
		responsibility: "project typed Telegram controller actions and reconcile durable delivery receipts",
		allowedImports: []string{
			"internal/coordinator", "internal/domain", "internal/telegramcontroller", "internal/telegramflow", "internal/telegrampipeline", "internal/telegramrecoverycomposition", "internal/telegramstate", "internal/telegramui",
		},
		maxProductionLines: 500,
	},
	"internal/turnruntimecomposition": {
		responsibility: "assemble the controller-facing P4 turn path from explicit dependencies after durable state and safelog are open",
		allowedImports: []string{
			"internal/artifactruntimecomposition", "internal/config", "internal/domain", "internal/observability", "internal/observabilitycomposition", "internal/p4runtimecomposition", "internal/providerinputcomposition", "internal/safelog", "internal/sessionruntime", "internal/settings", "internal/storage", "internal/telegram", "internal/telegrambridge", "internal/telegramflow", "internal/turnprocessing",
		},
		maxProductionLines: 200,
	},
	"internal/runtimefactory": {
		responsibility: "construct provider runtimes behind application ports",
		allowedImports: []string{
			"internal/app", "internal/config", "internal/domain", "internal/processenv",
			"internal/sessionruntime",
		},
		maxProductionLines: 700,
	},
	"internal/runtimeprotocol": {
		responsibility:     "define the bounded provider runtime wire protocol",
		maxProductionLines: 1000,
	},
	"internal/providerinputcomposition": {
		responsibility:     "resolve durable attachment custody at the provider boundary without flattening local paths into prompt text",
		allowedImports:     []string{"internal/domain", "internal/sessionruntime", "internal/turnprocessing"},
		maxProductionLines: 250,
	},
	"internal/p4runtimecomposition": {
		responsibility: "compose opt-in P4 media and Screen runtime adapters",
		allowedImports: []string{
			"internal/config", "internal/domain", "internal/inputcomposition", "internal/mediaproduction", "internal/providerinputcomposition", "internal/screen", "internal/screenproduction", "internal/sessionruntime", "internal/settings", "internal/speech/parakeet", "internal/storage", "internal/telegram", "internal/turnprocessing",
		},
		maxProductionLines: 200,
	},
	"internal/observability": {
		responsibility:     "record safe terminal timing and operational measurements",
		allowedImports:     []string{"internal/safelog"},
		maxProductionLines: 300,
	},
	"internal/observabilitycomposition": {
		responsibility:     "instrument provider turn submission with safe terminal measurements",
		allowedImports:     []string{"internal/domain", "internal/observability", "internal/sessionruntime", "internal/turnprocessing"},
		maxProductionLines: 250,
	},
	"internal/safelog": {
		responsibility:     "write redacted retention-bounded operational logs",
		maxProductionLines: 700,
	},
	"internal/singlemachinecomposition": {
		responsibility: "compose the single-computer Bria process",
		allowedImports: []string{
			"internal/app", "internal/authcomposition", "internal/callbacktoken", "internal/claudestore", "internal/config", "internal/coordinator", "internal/domain", "internal/durablecomposition", "internal/durableflow", "internal/interactioncomposition", "internal/messagejournal", "internal/recoverycomposition", "internal/recoveryruntime", "internal/runtimefactory", "internal/safelog", "internal/sessionexpiry", "internal/sessionid", "internal/sessionruntime", "internal/sessionsupervisor", "internal/settings", "internal/settingscomposition", "internal/storage", "internal/supervisioncomposition", "internal/telegram", "internal/telegrambridge", "internal/telegramcontroller", "internal/telegramflow", "internal/telegramnotify", "internal/telegrampipeline", "internal/telegramrecoverycomposition", "internal/telegramruntimecomposition", "internal/turnruntimecomposition", "internal/workdir",
		},
		maxProductionLines: 800,
	},
	"internal/secretfile": {
		responsibility:     "pass a bounded secret file to a callback with guaranteed transient zeroization",
		maxProductionLines: 200,
	},
	"internal/screen": {
		responsibility:     "render bounded virtual terminal snapshots from typed runtime events",
		allowedImports:     []string{"internal/domain", "internal/sessionruntime"},
		maxProductionLines: 500,
	},
	"internal/screenproduction": {
		responsibility:     "project typed provider events into virtual screen and optional Telegram media",
		allowedImports:     []string{"internal/screen", "internal/settings", "internal/telegram", "internal/turnprocessing"},
		maxProductionLines: 200,
	},
	"internal/sessioncatalog": {
		responsibility:       "persist one origin-neutral archive of discovered provider sessions",
		allowedImports:       []string{"internal/domain", "internal/sessiondiscovery"},
		maxProductionLines:   500,
		externalOnlyEvidence: "telegram_codex_claude_e2e",
	},
	"internal/sessiondiscovery": {
		responsibility:     "discover resumable local provider sessions",
		allowedImports:     []string{"internal/domain"},
		maxProductionLines: 300,
	},
	"internal/sessiondiscovery/claudeindex": {
		responsibility:       "adapt Claude transcript summaries to session discovery",
		allowedImports:       []string{"internal/claudestore", "internal/domain", "internal/sessiondiscovery"},
		maxProductionLines:   200,
		externalOnlyEvidence: "telegram_codex_claude_e2e",
	},
	"internal/sessionid": {
		responsibility:     "generate collision-resistant session identifiers",
		allowedImports:     []string{"internal/app", "internal/domain"},
		maxProductionLines: 700,
	},
	"internal/sessionruntime": {
		responsibility: "supervise provider adapter sessions",
		allowedImports: []string{
			"internal/app", "internal/domain", "internal/processgroup", "internal/runtimeprotocol",
		},
		maxProductionLines: 1300,
	},
	"internal/sessionsupervisor": {
		// Startup recovery of persisted sessions is the same lifecycle
		// responsibility; provider-specific reads stay behind its port.
		responsibility:     "reconcile provider exits with recoverable session lifecycle",
		allowedImports:     []string{"internal/app", "internal/domain"},
		maxProductionLines: 450,
	},
	"internal/sessionexpiry": {
		responsibility:     "schedule lifecycle closure for expired sessions",
		allowedImports:     []string{"internal/domain"},
		maxProductionLines: 300,
	},
	"internal/settings": {
		responsibility:     "persist and validate user settings",
		allowedImports:     []string{"internal/settingsport"},
		maxProductionLines: 700,
	},
	"internal/settingscomposition": {
		responsibility:     "compose neutral Telegram settings ports with canonical local settings and configuration stores",
		allowedImports:     []string{"internal/config", "internal/domain", "internal/settings", "internal/settingsport"},
		maxProductionLines: 200,
	},
	"internal/settingsport": {
		responsibility:     "define the storage-neutral preferences boundary used by Telegram control surfaces",
		allowedImports:     []string{"internal/domain"},
		maxProductionLines: 100,
	},
	"internal/speech": {
		responsibility:     "define local speech recognition boundary",
		maxProductionLines: 200,
	},
	"internal/speech/parakeet": {
		responsibility:     "run the local Parakeet recognizer",
		allowedImports:     []string{"internal/speech"},
		maxProductionLines: 300,
	},
	"internal/storage": {
		responsibility: "persist coordinator and session state",
		allowedImports: []string{
			"internal/archiveimport", "internal/coordinator", "internal/domain", "internal/telegramstate",
		},
		maxProductionLines: 1600,
	},
	"internal/supervisioncomposition": {
		responsibility:     "compose startup and live supervision for exact local provider bindings",
		allowedImports:     []string{"internal/app", "internal/domain", "internal/sessionsupervisor"},
		maxProductionLines: 400,
	},
	"internal/telegram": {
		responsibility:     "implement the Telegram HTTP transport",
		maxProductionLines: 1300,
	},
	"internal/telegramapp": {
		responsibility: "translate Telegram intents into application commands",
		allowedImports: []string{
			"internal/app", "internal/coordinator", "internal/domain", "internal/telegramui",
		},
		maxProductionLines:   700,
		externalOnlyEvidence: "telegram_codex_claude_e2e",
	},
	"internal/telegrambridge": {
		responsibility: "adapt Telegram transport payloads and callback tokens",
		allowedImports: []string{
			"internal/callbacktoken", "internal/coordinator", "internal/telegram", "internal/telegramrecovery", "internal/telegramrecovery/statusrecovery", "internal/telegramui",
		},
		maxProductionLines: 1100,
	},
	"internal/telegramcards": {
		responsibility:       "render Telegram session cards",
		allowedImports:       []string{"internal/domain", "internal/telegramui"},
		maxProductionLines:   400,
		externalOnlyEvidence: "telegram_codex_claude_e2e",
	},
	"internal/telegramcontroller": {
		responsibility: "coordinate Telegram session interactions",
		allowedImports: []string{
			"internal/app", "internal/coordinator", "internal/domain", "internal/sessionruntime", "internal/settingsport", "internal/telegramsettings", "internal/turnprocessing",
		},
		maxProductionLines: 2800,
	},
	"internal/telegramflow": {
		responsibility: "join Telegram callback, presentation, and durable card boundaries",
		allowedImports: []string{
			"internal/callbacktoken", "internal/coordinator", "internal/domain", "internal/telegram", "internal/telegrambridge",
			"internal/telegramops", "internal/telegrampipeline", "internal/telegramrecovery", "internal/telegramrecovery/statusrecovery", "internal/telegramstate", "internal/telegramui",
		},
		maxProductionLines: 2000,
	},
	"internal/telegramnotify": {
		responsibility: "deliver final and background Telegram notifications",
		allowedImports: []string{
			"internal/domain", "internal/telegram", "internal/telegramcontroller", "internal/telegramui",
		},
		maxProductionLines: 800,
	},
	"internal/telegramops": {
		responsibility:     "persist a bounded atomic opaque Telegram operation ledger",
		maxProductionLines: 500,
	},
	"internal/telegramrecovery": {
		responsibility:     "project owner-visible warnings and actions for unknown callback outcomes",
		allowedImports:     []string{"internal/domain", "internal/telegramui"},
		maxProductionLines: 100,
	},
	"internal/telegrampipeline": {
		responsibility: "persist and execute Telegram callback/update pipeline",
		allowedImports: []string{
			"internal/coordinator", "internal/domain", "internal/telegrambridge",
			"internal/telegramrecovery", "internal/telegramrecovery/statusrecovery", "internal/telegramstate", "internal/telegramui",
		},
		maxProductionLines: 1500,
	},
	"internal/telegramsessions": {
		responsibility:       "select and paginate Telegram-visible sessions",
		allowedImports:       []string{"internal/domain"},
		maxProductionLines:   300,
		externalOnlyEvidence: "telegram_codex_claude_e2e",
	},
	"internal/telegramstate": {
		responsibility:     "persist Telegram presentation state",
		allowedImports:     []string{"internal/domain"},
		maxProductionLines: 500,
	},
	"internal/telegramsettings": {
		responsibility:     "render and apply Telegram settings surfaces through neutral preferences ports",
		allowedImports:     []string{"internal/domain", "internal/settingsport"},
		maxProductionLines: 200,
	},
	"internal/telegramui": {
		responsibility:     "define transport-neutral Telegram views",
		allowedImports:     []string{"internal/app", "internal/domain"},
		maxProductionLines: 700,
	},
	"internal/turnprocessing": {
		responsibility:     "process one exact provider turn with durable acceptance and attachment custody",
		allowedImports:     []string{"internal/domain", "internal/sessionruntime"},
		maxProductionLines: 300,
	},
	"internal/update": {
		responsibility:       "verify releases and orchestrate safe updates",
		maxProductionLines:   700,
		externalOnlyEvidence: "update_and_forced_rollback",
	},
	"internal/updatecomposition": {
		responsibility:       "bind explicit update triggers to the durable signed-release flow",
		allowedImports:       []string{"internal/update", "internal/updateflow", "internal/updateinstall"},
		maxProductionLines:   400,
		externalOnlyEvidence: "update_and_forced_rollback",
	},
	"internal/updateflow": {
		responsibility:       "orchestrate signed staged updates and rollback receipts",
		allowedImports:       []string{"internal/update"},
		maxProductionLines:   1200,
		externalOnlyEvidence: "update_and_forced_rollback",
	},
	"internal/updateruntime": {
		responsibility:       "select release sources and stage verified updates through durable runtime state",
		allowedImports:       []string{"internal/update", "internal/updateflow", "internal/updateinstall"},
		maxProductionLines:   900,
		externalOnlyEvidence: "update_and_forced_rollback",
	},
	"internal/updateinstall": {
		responsibility:       "install packaged releases under an exact lock with state, operation, integrity, and postflight proof",
		allowedImports:       []string{"internal/update", "internal/updateflow"},
		maxProductionLines:   800,
		externalOnlyEvidence: "update_and_forced_rollback",
	},
	"internal/workdir": {
		responsibility:     "validate provider working directories",
		allowedImports:     []string{"internal/app", "internal/domain"},
		maxProductionLines: 200,
	},
}

func checkArchitecture(root string) []string {
	packages, err := goListPackages(root)
	if err != nil {
		return []string{err.Error()}
	}
	return checkGraph(packages)
}

func goListPackages(root string) ([]packageInfo, error) {
	command := exec.Command("go", "list", "-mod=readonly", "-json", "./...")
	command.Dir = root
	var standardOutput bytes.Buffer
	var standardError bytes.Buffer
	command.Stdout = &standardOutput
	command.Stderr = &standardError
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(standardError.String())
		if detail == "" {
			detail = strings.TrimSpace(standardOutput.String())
		}
		if detail == "" {
			detail = "unknown error"
		}
		return nil, fmt.Errorf("go list failed: %s", detail)
	}

	type moduleRecord struct {
		Path string
		Main bool
	}
	type goPackageRecord struct {
		ImportPath   string
		Dir          string
		GoFiles      []string
		CgoFiles     []string
		Module       *moduleRecord
		Imports      []string
		TestImports  []string
		XTestImports []string
	}
	decoder := json.NewDecoder(&standardOutput)
	var records []goPackageRecord
	for {
		var record goPackageRecord
		if err := decoder.Decode(&record); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("decode go list output: %w", err)
		}
		records = append(records, record)
	}
	if len(records) == 0 {
		return nil, nil
	}
	modulePaths := make(map[string]struct{})
	for _, record := range records {
		if record.Module != nil && record.Module.Main {
			modulePaths[record.Module.Path] = struct{}{}
		}
	}
	if len(modulePaths) != 1 {
		return nil, errors.New("go list did not report exactly one main module for local packages")
	}
	modulePath := ""
	for candidate := range modulePaths {
		modulePath = candidate
	}

	packages := make([]packageInfo, 0, len(records))
	for _, record := range records {
		if record.ImportPath == "" {
			return nil, errors.New("go list package record has no ImportPath")
		}
		relative, err := relativePackagePath(record.ImportPath, modulePath)
		if err != nil {
			return nil, err
		}
		productionFiles := append(append([]string(nil), record.GoFiles...), record.CgoFiles...)
		productionLines, err := countProductionLines(record.Dir, productionFiles)
		if err != nil {
			return nil, fmt.Errorf("measure package %s: %w", relative, err)
		}
		packages = append(packages, packageInfo{
			ImportPath:      record.ImportPath,
			RelativePath:    relative,
			Imports:         sortedUnique(record.Imports),
			TestImports:     sortedUnique(record.TestImports),
			XTestImports:    sortedUnique(record.XTestImports),
			HasProduction:   len(productionFiles) > 0,
			ProductionLines: productionLines,
		})
	}
	return packages, nil
}

func countProductionLines(directory string, filenames []string) (int, error) {
	total := 0
	for _, filename := range filenames {
		data, err := os.ReadFile(filepath.Join(directory, filename))
		if err != nil {
			return 0, err
		}
		if len(data) == 0 {
			continue
		}
		total += bytes.Count(data, []byte{'\n'})
		if data[len(data)-1] != '\n' {
			total++
		}
	}
	return total, nil
}

func relativePackagePath(importPath, modulePath string) (string, error) {
	if importPath == modulePath {
		return ".", nil
	}
	prefix := modulePath + "/"
	if !strings.HasPrefix(importPath, prefix) {
		return "", fmt.Errorf("package %q is outside reported module %q", importPath, modulePath)
	}
	return strings.TrimPrefix(importPath, prefix), nil
}

func checkGraph(packages []packageInfo) []string {
	localPaths := make(map[string]string, len(packages))
	packagesByPath := make(map[string]packageInfo, len(packages))
	for _, pkg := range packages {
		localPaths[pkg.ImportPath] = pkg.RelativePath
		packagesByPath[pkg.RelativePath] = pkg
	}
	problems := make(map[string]struct{})
	for _, pkg := range packages {
		source := pkg.RelativePath
		policy, registered := packagePolicies[source]
		if pkg.HasProduction {
			if beginsWith(source, "internal") && !registered {
				problems["unregistered internal production package: "+source] = struct{}{}
			}
			if registered {
				if strings.TrimSpace(policy.responsibility) == "" || policy.maxProductionLines <= 0 {
					problems["invalid architecture registry entry: "+source] = struct{}{}
				}
				if policy.externalOnlyEvidence != "" && !containsPackage(mandatoryReleaseEvidence, policy.externalOnlyEvidence) {
					problems["invalid external-only evidence category: "+source] = struct{}{}
				}
				if pkg.ProductionLines > policy.maxProductionLines {
					problems[fmt.Sprintf(
						"production size exceeds registered responsibility budget: %s has %d lines, limit %d",
						source,
						pkg.ProductionLines,
						policy.maxProductionLines,
					)] = struct{}{}
				}
			}
		}
		for _, part := range strings.Split(source, "/") {
			if _, forbidden := forbiddenPackageDirectories[part]; forbidden {
				problems[fmt.Sprintf("forbidden package directory %q: %s", part, source)] = struct{}{}
			}
		}
		for _, candidate := range packageImports(pkg) {
			imported := candidate.path
			target, local := localPaths[imported]
			if !local {
				continue
			}
			// An external test package imports the package it tests. That edge is
			// test scaffolding, not a production dependency or an architecture
			// path into the package.
			if candidate.origin == importExternalTest && target == source {
				continue
			}
			edge := source + " -> " + target
			if candidate.origin == importProduction && registered && target != source &&
				!containsPackage(policy.allowedImports, target) {
				if policy.compositionRoot {
					problems["composition root imports dependency outside registered boundary: "+edge] = struct{}{}
				} else {
					problems["package imports dependency outside registered boundary: "+edge] = struct{}{}
				}
			}
			if !beginsWith(source, "cmd") && beginsWith(target, "cmd") {
				problems["non-cmd package imports cmd: "+edge] = struct{}{}
			}
			if beginsWith(source, "internal/domain") {
				problems["domain imports another Bria package: "+edge] = struct{}{}
			}
			if beginsWith(source, "internal/app") &&
				!beginsWith(target, "internal/app") &&
				!beginsWith(target, "internal/domain") {
				problems["app imports infrastructure: "+edge] = struct{}{}
			}
			codexToClaude := beginsWith(source, "internal/provider/codex") && beginsWith(target, "internal/provider/claude")
			claudeToCodex := beginsWith(source, "internal/provider/claude") && beginsWith(target, "internal/provider/codex")
			if codexToClaude || claudeToCodex {
				problems["provider adapters import each other: "+edge] = struct{}{}
			}
			if beginsWith(source, "internal/telegramapp") && beginsWithAny(target, telegramAppForbiddenImports) {
				problems["telegram app imports concrete adapter: "+edge] = struct{}{}
			}
			if beginsWith(source, "internal/config") &&
				!allowedPackageImport(target, "internal/config", configAllowedImports) {
				problems["config imports package outside domain: "+edge] = struct{}{}
			}
			if beginsWith(source, "internal/workdir") &&
				!allowedPackageImport(target, "internal/workdir", workdirAllowedImports) {
				problems["workdir validator imports concrete component: "+edge] = struct{}{}
			}
			if beginsWith(source, "internal/telegram") &&
				!allowedPackageImport(target, "internal/telegram", nil) {
				problems["Telegram transport imports Bria product package: "+edge] = struct{}{}
			}
			if beginsWith(source, "internal/telegramui") &&
				!allowedPackageImport(target, "internal/telegramui", telegramUIAllowedImports) {
				problems["telegram UI imports transport or control package: "+edge] = struct{}{}
			}
			for _, providerRoot := range []string{"internal/provider/codex", "internal/provider/claude"} {
				if beginsWith(source, providerRoot) &&
					!allowedProviderAdapterImport(providerRoot, target) {
					problems["provider adapter imports concrete component: "+edge] = struct{}{}
				}
			}
			for _, infrastructureRoot := range processInfrastructureRoots {
				if beginsWith(source, infrastructureRoot) &&
					!allowedPackageImport(target, infrastructureRoot, nil) {
					problems["process infrastructure imports Bria package: "+edge] = struct{}{}
				}
			}
			if beginsWith(source, "internal/runtimefactory") &&
				!allowedRuntimeFactoryImport(target, candidate.origin) {
				problems["runtime factory imports package outside its composition boundary: "+edge] = struct{}{}
			}
			if beginsWith(source, "internal/sessionruntime") &&
				!allowedPackageImport(target, "internal/sessionruntime", sessionRuntimeAllowedImports) {
				problems["session runtime imports concrete product or Telegram package: "+edge] = struct{}{}
			}
			if beginsWith(source, "internal/telegramcontroller") &&
				!allowedPackageImport(target, "internal/telegramcontroller", telegramControllerAllowedImports) {
				problems["Telegram controller imports concrete adapter: "+edge] = struct{}{}
			}
			if beginsWith(source, "internal/telegrambridge") &&
				!allowedPackageImport(target, "internal/telegrambridge", telegramBridgeAllowedImports) {
				problems["Telegram bridge imports package outside transport adaptation: "+edge] = struct{}{}
			}
			if beginsWith(source, "internal/telegramnotify") &&
				!allowedPackageImport(target, "internal/telegramnotify", telegramNotifyAllowedImports) {
				problems["Telegram notifier imports package outside delivery boundary: "+edge] = struct{}{}
			}
			if beginsWith(source, "internal/coordinator") &&
				!allowedPackageImport(target, "internal/coordinator", nil) {
				problems["coordinator imports product or transport package: "+edge] = struct{}{}
			}
			if beginsWith(source, "internal/sessionid") && beginsWithAny(target, sessionIDForbiddenImports) {
				problems["session id source imports infrastructure: "+edge] = struct{}{}
			}
			if beginsWith(source, "internal/nodelink") && beginsWith(target, "internal/storage") {
				problems["node link bypasses application storage boundary: "+edge] = struct{}{}
			}
		}
	}
	for _, orphan := range orphanProductionPackages(packagesByPath, localPaths) {
		problems["orphan production package has no path from a composition root: "+orphan] = struct{}{}
	}
	for source := range productionReachablePackages(packagesByPath, localPaths) {
		if policy, registered := packagePolicies[source]; registered && policy.externalOnlyEvidence != "" {
			problems["reachable production package remains marked external-only: "+source] = struct{}{}
		}
	}
	result := make([]string, 0, len(problems))
	for problem := range problems {
		result = append(result, problem)
	}
	sort.Strings(result)
	return result
}

func containsPackage(packages []string, target string) bool {
	for _, allowed := range packages {
		if target == allowed {
			return true
		}
	}
	return false
}

func orphanProductionPackages(packages map[string]packageInfo, localPaths map[string]string) []string {
	reachable := productionReachablePackages(packages, localPaths)
	if reachable == nil {
		return nil
	}

	var orphans []string
	for source, pkg := range packages {
		if !pkg.HasProduction || !beginsWith(source, "internal") {
			continue
		}
		policy, registered := packagePolicies[source]
		if !registered || policy.externalOnlyEvidence != "" {
			continue
		}
		if _, exists := reachable[source]; !exists {
			orphans = append(orphans, source)
		}
	}
	sort.Strings(orphans)
	return orphans
}

func productionReachablePackages(packages map[string]packageInfo, localPaths map[string]string) map[string]struct{} {
	var roots []string
	for source, pkg := range packages {
		policy, registered := packagePolicies[source]
		if registered && policy.compositionRoot && pkg.HasProduction {
			roots = append(roots, source)
		}
	}
	if len(roots) == 0 {
		return nil
	}

	reachable := make(map[string]struct{}, len(packages))
	queue := append([]string(nil), roots...)
	for len(queue) > 0 {
		source := queue[0]
		queue = queue[1:]
		if _, seen := reachable[source]; seen {
			continue
		}
		reachable[source] = struct{}{}
		pkg, exists := packages[source]
		if !exists {
			continue
		}
		for _, imported := range pkg.Imports {
			target, local := localPaths[imported]
			if local {
				queue = append(queue, target)
			}
		}
	}

	return reachable
}

func packageImports(pkg packageInfo) []packageImport {
	result := make([]packageImport, 0, len(pkg.Imports)+len(pkg.TestImports)+len(pkg.XTestImports))
	for _, group := range []struct {
		imports []string
		origin  importOrigin
	}{
		{imports: pkg.Imports, origin: importProduction},
		{imports: pkg.TestImports, origin: importInternalTest},
		{imports: pkg.XTestImports, origin: importExternalTest},
	} {
		for _, imported := range group.imports {
			result = append(result, packageImport{path: imported, origin: group.origin})
		}
	}
	return result
}

func sortedUnique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func beginsWith(packagePath, prefix string) bool {
	return packagePath == prefix || strings.HasPrefix(packagePath, prefix+"/")
}

func beginsWithAny(packagePath string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if beginsWith(packagePath, prefix) {
			return true
		}
	}
	return false
}

func allowedPackageImport(target, ownRoot string, allowed []string) bool {
	return beginsWith(target, ownRoot) || beginsWithAny(target, allowed)
}

func allowedProviderAdapterImport(providerRoot, target string) bool {
	if allowedPackageImport(target, providerRoot, providerAdapterAllowedImports) {
		return true
	}
	// Both provider packages implement the shared authorization port: Codex via
	// its interactive CLI protocol and Claude via its explicit bare API-key mode.
	if (providerRoot == "internal/provider/codex" || providerRoot == "internal/provider/claude") && target == "internal/authflow" {
		return true
	}
	if providerRoot == "internal/provider/codex" && target == "internal/sessiondiscovery" {
		return true
	}
	if (providerRoot == "internal/provider/codex" || providerRoot == "internal/provider/claude") && target == "internal/sessionsupervisor" {
		return true
	}
	return (providerRoot == "internal/provider/codex" || providerRoot == "internal/provider/claude") &&
		target == "internal/runtimeprotocol"
}

func allowedRuntimeFactoryImport(target string, origin importOrigin) bool {
	if allowedPackageImport(target, "internal/runtimefactory", runtimeFactoryAllowedImports) {
		return true
	}
	return origin == importExternalTest &&
		(target == "internal/provider/codex" || target == "internal/provider/claude")
}

func hasAnyPrefix(value string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func hasAnySuffix(value string, suffixes []string) bool {
	for _, suffix := range suffixes {
		if strings.HasSuffix(value, suffix) {
			return true
		}
	}
	return false
}

func commandExitCode(directory, name string, arguments ...string) (int, error) {
	command := exec.Command(name, arguments...)
	command.Dir = directory
	err := command.Run()
	if err == nil {
		return 0, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode(), nil
	}
	return -1, err
}

func findRepositoryRoot() (string, error) {
	current, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(current, "go.mod")); err == nil {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", errors.New("repository root containing go.mod was not found")
		}
		current = parent
	}
}

func selectedCheckNames(selected string) []string {
	if selected == "all" {
		return []string{"policy", "links", "filenames", "architecture"}
	}
	return []string{selected}
}

func run() int {
	root, err := findRepositoryRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		return 1
	}
	selected := "policy"
	if len(os.Args) > 2 {
		fmt.Fprintln(os.Stderr, "usage: check_repo [policy|links|filenames|architecture|release-evidence|all]")
		return 2
	}
	if len(os.Args) == 2 {
		selected = os.Args[1]
	}

	type namedCheck struct {
		name    string
		success string
		check   func(string) []string
	}
	checks := []namedCheck{
		{name: "policy", success: "AGENTS.md policy structure: OK", check: checkPolicy},
		{name: "links", success: "Local Markdown links: OK", check: checkLinks},
		{name: "filenames", success: "Tracked filenames and source secret scan: OK", check: checkFilenames},
		{name: "architecture", success: "Go package architecture: OK", check: checkArchitecture},
		{name: "release-evidence", success: "Mandatory release evidence: OK", check: checkReleaseEvidence},
	}
	selectedNames := selectedCheckNames(selected)
	known := selected == "all"
	exitCode := 0
	for _, candidate := range checks {
		if !containsPackage(selectedNames, candidate.name) {
			continue
		}
		known = true
		problems := candidate.check(root)
		if len(problems) == 0 {
			fmt.Println(candidate.success)
			continue
		}
		exitCode = 1
		for _, problem := range problems {
			fmt.Fprintln(os.Stderr, "ERROR:", problem)
		}
	}
	if !known {
		fmt.Fprintln(os.Stderr, "usage: check_repo [policy|links|filenames|architecture|release-evidence|all]")
		return 2
	}
	return exitCode
}

func main() {
	os.Exit(run())
}
