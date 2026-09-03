package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

var requiredInvariantLines = []string{
	"- единый неизменный договор задачи до начала работы;",
	"- карта покрытия до параллельного запуска;",
	"- максимальная безопасная параллельность и непересекающееся владение;",
	"- отдельные рабочие копии для конфликтующих изменений;",
	"- единственный владелец объединения;",
	"- проверка доказательств и явное описание неопределённости;",
	"- быстрые профильные проверки во время разработки и полный набор перед выпуском;",
	"- проверка физического результата;",
	"- предварительный перечень, отдельное подтверждение и повторное чтение для внешних изменений.",
}

var requiredPolicyClauseLines = map[string][]string{
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
		"Перед каждым таким действием:",
		"1. Показать точную цель и перечень изменений.",
		"2. Получить отдельное однозначное подтверждение пользователя.",
		"3. Выполнить только подтверждённое действие.",
		"4. Повторно прочитать целевое состояние и проверить фактический результат.",
		"Одно подтверждение не распространяется на последующие действия.",
		"Неопределённый сетевой ответ не считать ни успехом, ни отказом: сначала безопасно перечитать состояние, затем решать вопрос о повторе.",
	},
}

func TestPolicyCheckerEnforcesEveryMandatoryInvariant(t *testing.T) {
	original := readProjectPolicy(t)
	for invariantIndex, invariant := range requiredInvariantLines {
		invariant := invariant
		t.Run(fmt.Sprintf("invariant/%02d", invariantIndex), func(t *testing.T) {
			if !strings.Contains(original, invariant) {
				t.Fatalf("project AGENTS.md does not contain fixture invariant %q", invariant)
			}
			repo := makeRepo(t, strings.Replace(original, invariant, "", 1))
			assertErrorContains(t, checkPolicy(repo), "mandatory invariant")
		})
	}
}

func TestPolicyCheckerRejectsMutationOfEveryRequiredClause(t *testing.T) {
	original := readProjectPolicy(t)
	for section, clauses := range requiredPolicyClauseLines {
		for clauseIndex, clause := range clauses {
			section, clause := section, clause
			t.Run(fmt.Sprintf("%s/%02d", section, clauseIndex), func(t *testing.T) {
				if !strings.Contains(original, clause) {
					t.Fatalf("project AGENTS.md does not contain fixture clause %q", clause)
				}
				repo := makeRepo(t, strings.Replace(original, clause, "", 1))
				assertErrorContains(t, checkPolicy(repo), "required clause")
			})
		}
	}
}

func TestPolicyCheckerAllowsUntrackedPolicyBeforeFirstCommit(t *testing.T) {
	repo := makeRepo(t, readProjectPolicy(t))
	if errors := checkPolicy(repo); len(errors) != 0 {
		t.Fatalf("checkPolicy() errors = %v, want none", errors)
	}
}

func TestPolicyCheckerRequiresTrackedPolicyAfterFirstCommit(t *testing.T) {
	repo := makeRepo(t, readProjectPolicy(t))
	writeFile(t, filepath.Join(repo, "README.md"), "# Test repository\n")
	runGit(t, repo, "add", "README.md")
	runGit(
		t,
		repo,
		"-c", "user.name=Bria Tests",
		"-c", "user.email=bria-tests@example.invalid",
		"commit", "--quiet", "-m", "initial",
	)
	assertErrorContains(t, checkPolicy(repo), "must be tracked")
}

func TestPolicyCheckerRejectsMissingSectionAndRequiredConcept(t *testing.T) {
	original := readProjectPolicy(t)
	repoWithoutSection := makeRepo(
		t,
		strings.Replace(original, "## Task contract", "## Removed task contract", 1),
	)
	assertErrorContains(t, checkPolicy(repoWithoutSection), "missing required section '## Task contract'")

	repoWithoutConcept := makeRepo(
		t,
		strings.Replace(original, "- источник требований и доказательств;", "- origin requirements;", 1),
	)
	assertErrorContains(t, checkPolicy(repoWithoutConcept), "does not state the required concept")
}

func TestLinkCheckerIgnoresMarkdownExcludedByGit(t *testing.T) {
	repo := makeRepo(t, readProjectPolicy(t))
	writeFile(t, filepath.Join(repo, ".gitignore"), "/.agents/\n/.claude/\n/.codex/\n")
	for _, name := range []string{".agents", ".claude", ".codex"} {
		writeFile(t, filepath.Join(repo, name, "context.md"), "[private](missing.md)\n")
	}
	if errors := checkLinks(repo); len(errors) != 0 {
		t.Fatalf("checkLinks() errors = %v, want none", errors)
	}
}

func TestLinkCheckerIncludesUntrackedNonIgnoredMarkdown(t *testing.T) {
	repo := makeRepo(t, readProjectPolicy(t))
	writeFile(t, filepath.Join(repo, "notes.md"), "[broken](missing.md)\n")
	assertErrorContains(t, checkLinks(repo), "notes.md:1: missing link target")
}

func TestLinkCheckerRejectsRepositoryEscape(t *testing.T) {
	repo := makeRepo(t, readProjectPolicy(t))
	writeFile(t, filepath.Join(repo, "notes.md"), "[escape](../outside.md)\n")
	assertErrorContains(t, checkLinks(repo), "link leaves repository")
}

func TestFilenameCheckerRejectsEveryForbiddenClass(t *testing.T) {
	tests := map[string]string{
		"directory":       "runtime/data.txt",
		"exact":           "auth.json",
		"env":             ".env.local",
		"suffix":          "owner.token",
		"language source": "scripts/check.py",
	}
	for name, relativePath := range tests {
		name, relativePath := name, relativePath
		t.Run(name, func(t *testing.T) {
			repo := makeRepo(t, readProjectPolicy(t))
			writeFile(t, filepath.Join(repo, relativePath), "fixture\n")
			runGit(t, repo, "add", "-f", relativePath)
			assertErrorContains(t, checkFilenames(repo), "tracked")
		})
	}
}

func TestFilenameCheckerRejectsTrackedAvisCacheAndRootBinaries(t *testing.T) {
	for _, relativePath := range []string{
		".avis/go-build-cache",
		"bria",
		"bria-codex-adapter",
		"bria-claude-adapter",
		"bria-container-preflight",
	} {
		t.Run(relativePath, func(t *testing.T) {
			repo := makeRepo(t, readProjectPolicy(t))
			writeFile(t, filepath.Join(repo, relativePath), "fixture\n")
			runGit(t, repo, "add", "-f", relativePath)
			assertErrorContains(t, checkFilenames(repo), "tracked")
		})
	}
}

func TestFilenameCheckerAllowsReleaseArtifactBinaryUnderDist(t *testing.T) {
	repo := makeRepo(t, readProjectPolicy(t))
	path := filepath.Join(repo, "dist/1.0.0/bria")
	writeFile(t, path, "release fixture\n")
	runGit(t, repo, "add", "-f", "dist/1.0.0/bria")
	if errors := checkFilenames(repo); len(errors) != 0 {
		t.Fatalf("checkFilenames() errors = %v, want none for dist release artifact", errors)
	}
}

func TestFilenameCheckerAllowsSourcePackageNamedAfterRootRuntimeDirectory(t *testing.T) {
	repo := makeRepo(t, readProjectPolicy(t))
	writeFile(t, filepath.Join(repo, "internal/backup/backup.go"), "package backup\n")
	runGit(t, repo, "add", "internal/backup/backup.go")
	if errors := checkFilenames(repo); len(errors) != 0 {
		t.Fatalf("checkFilenames() errors = %v, want none", errors)
	}
}

func TestSecretContentCheckerRejectsHighConfidenceCredentialInSource(t *testing.T) {
	repo := makeRepo(t, readProjectPolicy(t))
	credential := "sk-" + strings.Repeat("sensitive", 4)
	writeFile(t, filepath.Join(repo, "internal/example/config.go"), "package example\nconst credential = \""+credential+"\"\n")
	assertErrorContains(t, checkFilenames(repo), "probable secret content")
}

func TestSecretContentCheckerRejectsCredentialInsideReleaseArchive(t *testing.T) {
	repo := makeRepo(t, readProjectPolicy(t))
	archivePath := filepath.Join(repo, "dist", "1.0.0", "bria_1.0.0_linux_amd64.tar.gz")
	credential := "123456789:" + strings.Repeat("A", 35)
	writeTarGZ(t, archivePath, "bria_1.0.0_linux_amd64/config.txt", []byte(credential))
	assertErrorContains(t, checkSecretContents(repo), "probable secret content in release archive")
}

func TestSecretContentCheckerAllowsPlaceholdersAndOrdinarySource(t *testing.T) {
	repo := makeRepo(t, readProjectPolicy(t))
	writeFile(t, filepath.Join(repo, "config.example.json"), "{\"telegram_token\":\"${TELEGRAM_TOKEN}\"}\n")
	if errors := checkSecretContents(repo); len(errors) != 0 {
		t.Fatalf("checkSecretContents() errors = %v, want none", errors)
	}
}

func TestArchitectureCheckerRejectsForbiddenPackageDirectory(t *testing.T) {
	errors := checkGraph([]packageInfo{testPackage("internal/common")})
	assertErrorContains(t, errors, "forbidden package directory \"common\"")
}

func TestArchitectureCheckerRejectsUnregisteredInternalProductionPackage(t *testing.T) {
	errors := checkGraph([]packageInfo{testPackage("internal/newfeature")})
	assertErrorContains(t, errors, "unregistered internal production package: internal/newfeature")
}

func TestArchitectureCheckerRejectsCompositionRootDependencyOutsideRegistry(t *testing.T) {
	errors := checkGraph(graphWithEdge("cmd/bria", "internal/provider/codex"))
	assertErrorContains(t, errors, "composition root imports dependency outside registered boundary: cmd/bria -> internal/provider/codex")
}

func TestArchitectureCheckerReportsWiredPackageWithNoCompositionPath(t *testing.T) {
	errors := checkGraph([]packageInfo{
		testPackage("cmd/bria"),
		testPackage("internal/app"),
	})
	assertErrorContains(t, errors, "orphan production package has no path from a composition root: internal/app")
}

func TestArchitectureCheckerAllowsRegisteredPackageAwaitingComposition(t *testing.T) {
	packages := []packageInfo{
		testPackage("cmd/bria"),
		testPackage("internal/backup"),
	}
	if errors := checkGraph(packages); len(errors) != 0 {
		t.Fatalf("checkGraph() errors = %v, want none", errors)
	}
}

func TestArchitectureCheckerRegistersLandedFlowAndOperationsPackages(t *testing.T) {
	packages := []packageInfo{
		testPackage("internal/authflow"),
		testPackage("internal/backupflow", "internal/backup"),
		testPackage("internal/durableflow", "internal/messagejournal"),
		testPackage("internal/mediaflow", "internal/files", "internal/speech", "internal/telegram", "internal/telegramcontroller"),
		testPackage("internal/safelog"),
		testPackage("internal/sessiondiscovery", "internal/domain"),
		testPackage("internal/sessionexpiry", "internal/domain"),
		testPackage("internal/backup"),
		testPackage("internal/messagejournal"),
		testPackage("internal/files"),
		testPackage("internal/speech"),
		testPackage("internal/telegram"),
		testPackage("internal/telegramcontroller"),
		testPackage("internal/domain"),
	}
	if errors := checkGraph(packages); len(errors) != 0 {
		t.Fatalf("checkGraph() errors = %v, want none", errors)
	}
}

func TestArchitectureCheckerRejectsReachablePackageStillMarkedAwaitingComposition(t *testing.T) {
	original := packagePolicies["internal/sessionexpiry"]
	staged := original
	staged.externalOnlyEvidence = "concurrency_and_recovery"
	packagePolicies["internal/sessionexpiry"] = staged
	t.Cleanup(func() { packagePolicies["internal/sessionexpiry"] = original })

	errors := checkGraph([]packageInfo{
		testPackage("cmd/bria", "internal/sessionexpiry"),
		testPackage("internal/sessionexpiry", "internal/domain"),
		testPackage("internal/domain"),
	})
	assertErrorContains(t, errors, "reachable production package remains marked external-only")
}

func TestArchitectureCheckerTreatsActuallyWiredRuntimePackagesAsRequired(t *testing.T) {
	packages := []packageInfo{
		testPackage("cmd/bria", "internal/singlemachinecomposition"),
		testPackage("internal/singlemachinecomposition", "internal/runtimefactory", "internal/safelog", "internal/sessionexpiry"),
		testPackage("internal/runtimefactory", "internal/sessionruntime"),
		testPackage("internal/sessionruntime", "internal/runtimeprotocol"),
		testPackage("internal/runtimeprotocol"),
		testPackage("internal/safelog"),
		testPackage("internal/sessionexpiry", "internal/domain"),
		testPackage("internal/domain"),
	}
	if errors := checkGraph(packages); len(errors) != 0 {
		t.Fatalf("checkGraph() errors = %v, want none", errors)
	}
}

func TestArchitectureCheckerAcceptsLandedTelegramCompositionGraph(t *testing.T) {
	telegramOperations := testPackage("internal/telegramops")
	telegramOperations.ProductionLines = 450
	packages := []packageInfo{
		testPackage("cmd/bria", "internal/singlemachinecomposition"),
		testPackage("internal/singlemachinecomposition", "internal/callbacktoken", "internal/telegrambridge", "internal/telegramflow", "internal/telegrampipeline"),
		testPackage("internal/callbacktoken"),
		testPackage("internal/telegramflow", "internal/telegramops", "internal/telegramrecovery"),
		telegramOperations,
		testPackage("internal/telegrambridge", "internal/telegramrecovery"),
		testPackage("internal/telegramrecovery", "internal/domain", "internal/telegramui"),
		testPackage("internal/telegrampipeline", "internal/telegramrecovery"),
		testPackage("internal/domain"),
		testPackage("internal/telegramui"),
	}
	if errors := checkGraph(packages); len(errors) != 0 {
		t.Fatalf("checkGraph() errors = %v, want none", errors)
	}
}

func TestArchitectureCheckerAcceptsLandedDurableMessageComposition(t *testing.T) {
	packages := []packageInfo{
		testPackage("cmd/bria", "internal/singlemachinecomposition"),
		testPackage("internal/singlemachinecomposition", "internal/durableflow"),
		testPackage("internal/durableflow", "internal/messagejournal"),
		testPackage("internal/messagejournal"),
	}
	if errors := checkGraph(packages); len(errors) != 0 {
		t.Fatalf("checkGraph() errors = %v, want none", errors)
	}
}

func TestArchitectureCheckerRegistersCurrentCompositionBoundaries(t *testing.T) {
	tests := []struct {
		path           string
		responsibility string
		imports        []string
		limit          int
		evidence       string
	}{
		{
			path:           "internal/artifactretrycomposition",
			responsibility: "own the durable manual decision between an unconfirmed artifact delivery and its explicit retry callback",
			imports:        []string{"internal/artifactdelivery", "internal/artifactproduction", "internal/domain", "internal/telegrambridge", "internal/telegramflow", "internal/telegrampipeline", "internal/telegramui", "internal/turnprocessing"},
			limit:          700,
		},
		{
			path:           "internal/artifactruntimecomposition",
			responsibility: "assemble the opt-in artifact final and manual retry path from explicit P4 configuration only",
			imports:        []string{"internal/artifactcomposition", "internal/artifactproduction", "internal/artifactretrycomposition", "internal/config", "internal/secretfile", "internal/telegram", "internal/telegrambridge", "internal/telegramflow", "internal/turnprocessing"},
			limit:          200,
		},
		{
			path:           "internal/durablecomposition",
			responsibility: "compose durable message custody and accepted-turn reconciliation",
			imports:        []string{"internal/domain", "internal/durableflow", "internal/messagejournal", "internal/sessionsupervisor", "internal/telegramcontroller", "internal/telegramnotify"},
			limit:          500,
		},
		{
			path:           "internal/telegramruntimecomposition",
			responsibility: "project typed Telegram controller actions and reconcile durable delivery receipts",
			imports:        []string{"internal/coordinator", "internal/domain", "internal/telegramcontroller", "internal/telegramflow", "internal/telegrampipeline", "internal/telegramrecoverycomposition", "internal/telegramstate", "internal/telegramui"},
			limit:          500,
		},
		{
			path:           "internal/turnruntimecomposition",
			responsibility: "assemble the controller-facing P4 turn path from explicit dependencies after durable state and safelog are open",
			imports:        []string{"internal/artifactruntimecomposition", "internal/config", "internal/domain", "internal/observability", "internal/observabilitycomposition", "internal/p4runtimecomposition", "internal/providerinputcomposition", "internal/safelog", "internal/sessionruntime", "internal/settings", "internal/storage", "internal/telegram", "internal/telegrambridge", "internal/telegramflow", "internal/turnprocessing"},
			limit:          200,
		},
		{
			path:           "internal/singlemachinecomposition",
			responsibility: "compose the single-computer Bria process",
			imports: []string{
				"internal/app", "internal/authcomposition", "internal/callbacktoken", "internal/claudestore", "internal/config", "internal/coordinator", "internal/domain", "internal/durablecomposition", "internal/durableflow", "internal/interactioncomposition", "internal/messagejournal", "internal/recoverycomposition", "internal/recoveryruntime", "internal/runtimefactory", "internal/safelog", "internal/sessionexpiry", "internal/sessionid", "internal/sessionruntime", "internal/sessionsupervisor", "internal/settings", "internal/settingscomposition", "internal/storage", "internal/supervisioncomposition", "internal/telegram", "internal/telegrambridge", "internal/telegramcontroller", "internal/telegramflow", "internal/telegramnotify", "internal/telegrampipeline", "internal/telegramrecoverycomposition", "internal/telegramruntimecomposition", "internal/turnruntimecomposition", "internal/workdir",
			},
			limit: 800,
		},
		{
			path:           "internal/p4runtimecomposition",
			responsibility: "compose opt-in P4 media and Screen runtime adapters",
			imports:        []string{"internal/config", "internal/domain", "internal/inputcomposition", "internal/mediaproduction", "internal/providerinputcomposition", "internal/screen", "internal/screenproduction", "internal/sessionruntime", "internal/settings", "internal/speech/parakeet", "internal/storage", "internal/telegram", "internal/turnprocessing"},
			limit:          200,
		},
		{
			path:           "internal/observability",
			responsibility: "record safe terminal timing and operational measurements",
			imports:        []string{"internal/safelog"},
			limit:          300,
		},
	}

	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			policy, ok := packagePolicies[test.path]
			if !ok {
				t.Fatalf("packagePolicies[%q] is missing", test.path)
			}
			if policy.responsibility != test.responsibility {
				t.Fatalf("responsibility = %q, want %q", policy.responsibility, test.responsibility)
			}
			if policy.maxProductionLines != test.limit {
				t.Fatalf("maxProductionLines = %d, want %d", policy.maxProductionLines, test.limit)
			}
			if strings.Join(policy.allowedImports, "\x00") != strings.Join(test.imports, "\x00") {
				t.Fatalf("allowedImports = %v, want %v", policy.allowedImports, test.imports)
			}
			if policy.externalOnlyEvidence != test.evidence {
				t.Fatalf("externalOnlyEvidence = %q, want %q", policy.externalOnlyEvidence, test.evidence)
			}
		})
	}
}

func TestArchitectureCheckerUsesOnlyCurrentBriaCommandDirectImports(t *testing.T) {
	for _, imported := range []string{
		"internal/app", "internal/config", "internal/coordinator", "internal/domain", "internal/instancelock", "internal/sessionruntime", "internal/settings", "internal/singlemachinecomposition", "internal/storage", "internal/telegram", "internal/telegrambridge", "internal/telegramnotify",
	} {
		if errors := checkGraph(graphWithEdge("cmd/bria", imported)); len(errors) != 0 {
			t.Fatalf("current direct import %s rejected: %v", imported, errors)
		}
	}
	assertErrorContains(t, checkGraph(graphWithEdge("cmd/bria", "internal/telegramflow")), "composition root imports dependency outside registered boundary: cmd/bria -> internal/telegramflow")
}

func TestArchitectureCheckerAllowsWiredFinalAndObservabilityPackages(t *testing.T) {
	for _, path := range []string{"internal/artifactcomposition", "internal/artifactproduction", "internal/artifactdelivery"} {
		policy, ok := packagePolicies[path]
		if !ok {
			t.Fatalf("packagePolicies[%q] is missing", path)
		}
		if policy.externalOnlyEvidence != "" {
			t.Fatalf("packagePolicies[%q] remains external-only after public wiring", path)
		}
	}
	for _, path := range []string{"internal/observability", "internal/observabilitycomposition"} {
		if packagePolicies[path].externalOnlyEvidence != "" {
			t.Fatalf("packagePolicies[%q] remains external-only after public wiring", path)
		}
	}
}

func TestArchitectureCheckerRegistersObservabilityCompositionBoundary(t *testing.T) {
	policy, ok := packagePolicies["internal/observabilitycomposition"]
	if !ok {
		t.Fatal("packagePolicies[internal/observabilitycomposition] is missing")
	}
	if policy.responsibility != "instrument provider turn submission with safe terminal measurements" {
		t.Fatalf("responsibility = %q", policy.responsibility)
	}
	if strings.Join(policy.allowedImports, "\x00") != strings.Join([]string{"internal/domain", "internal/observability", "internal/sessionruntime", "internal/turnprocessing"}, "\x00") {
		t.Fatalf("allowedImports = %v", policy.allowedImports)
	}
	if policy.maxProductionLines != 250 || policy.externalOnlyEvidence != "" {
		t.Fatalf("policy = %#v", policy)
	}
	if errors := checkGraph([]packageInfo{
		testPackage("internal/observabilitycomposition", "internal/domain", "internal/observability", "internal/sessionruntime", "internal/turnprocessing"),
		testPackage("internal/domain"), testPackage("internal/observability", "internal/safelog"), testPackage("internal/safelog"), testPackage("internal/sessionruntime"), testPackage("internal/turnprocessing"),
	}); len(errors) != 0 {
		t.Fatalf("approved observability composition graph rejected: %v", errors)
	}
	assertErrorContains(t, checkGraph(graphWithEdge("internal/observabilitycomposition", "internal/telegram")), "package imports dependency outside registered boundary: internal/observabilitycomposition -> internal/telegram")
}

func TestReleaseEvidenceCheckerBlocksMissingManifest(t *testing.T) {
	assertErrorContains(t, validateReleaseEvidence(filepath.Join(t.TempDir(), "missing.json"), "1.2.3"), "evidence manifest is unavailable")
}

func TestReleaseEvidenceGateBlocksWhenManifestIsNotConfigured(t *testing.T) {
	t.Setenv("VERSION", "1.2.3")
	t.Setenv("BRIA_RELEASE_EVIDENCE_MANIFEST", "")
	assertErrorContains(t, checkReleaseEvidence(t.TempDir()), "must name an absolute evidence manifest")
}

func TestDevelopmentAllSelectionExcludesExternalReleaseEvidence(t *testing.T) {
	selected := strings.Join(selectedCheckNames("all"), ",")
	if strings.Contains(selected, "release-evidence") {
		t.Fatalf("selectedCheckNames(all) = %q, must keep external release evidence release-only", selected)
	}
	if !strings.Contains(strings.Join(selectedCheckNames("release-evidence"), ","), "release-evidence") {
		t.Fatal("explicit release-evidence selection must remain available")
	}
}

func TestExternalOnlyPackageAlwaysBlocksStrictReleaseEvidence(t *testing.T) {
	blockers := externalOnlyReleaseBlockers([]packageInfo{testPackage("internal/backup")})
	assertErrorContains(t, blockers, "external-only product package blocks release: internal/backup")
}

func TestReleaseEvidenceCheckerRequiresEveryMandatoryReceipt(t *testing.T) {
	directory := t.TempDir()
	manifest := writeEvidenceManifest(t, directory, "1.2.3", mandatoryReleaseEvidence[:len(mandatoryReleaseEvidence)-1])
	assertErrorContains(t, validateReleaseEvidence(manifest, "1.2.3"), "missing mandatory release evidence")
}

func TestReleaseEvidenceCheckerRejectsUnverifiedOrTamperedReceipt(t *testing.T) {
	directory := t.TempDir()
	manifest := writeEvidenceManifest(t, directory, "1.2.3", mandatoryReleaseEvidence)
	receipt := filepath.Join(directory, "receipts", mandatoryReleaseEvidence[0]+".txt")
	writeFile(t, receipt, "tampered\n")
	assertErrorContains(t, validateReleaseEvidence(manifest, "1.2.3"), "evidence receipt digest mismatch")
}

func TestReleaseEvidenceCheckerRejectsSymlinkReceipt(t *testing.T) {
	directory := t.TempDir()
	manifest := writeEvidenceManifest(t, directory, "1.2.3", mandatoryReleaseEvidence)
	relative := filepath.Join("receipts", mandatoryReleaseEvidence[0]+".txt")
	receipt := filepath.Join(directory, relative)
	contents, err := os.ReadFile(receipt)
	if err != nil {
		t.Fatalf("read receipt fixture: %v", err)
	}
	if err := os.Remove(receipt); err != nil {
		t.Fatalf("remove receipt fixture: %v", err)
	}
	realReceipt := filepath.Join(directory, "receipts", "real.txt")
	writeFile(t, realReceipt, string(contents))
	if err := os.Symlink(realReceipt, receipt); err != nil {
		t.Fatalf("symlink receipt fixture: %v", err)
	}
	assertErrorContains(t, validateReleaseEvidence(manifest, "1.2.3"), "release evidence file is invalid")
}

func TestReleaseEvidenceCheckerAcceptsCompleteVerifiedManifest(t *testing.T) {
	directory := t.TempDir()
	manifest := writeEvidenceManifest(t, directory, "1.2.3", mandatoryReleaseEvidence)
	if errors := validateReleaseEvidence(manifest, "1.2.3"); len(errors) != 0 {
		t.Fatalf("validateReleaseEvidence() errors = %v, want none", errors)
	}
}

func TestReleaseEvidenceCheckerBindsSourceRevision(t *testing.T) {
	directory := t.TempDir()
	manifest := writeEvidenceManifest(t, directory, "1.2.3", mandatoryReleaseEvidence)
	releaseManifest := filepath.Join(directory, "release-manifest.json")
	writeFile(t, releaseManifest, "signed release identity\n")
	assertErrorContains(
		t,
		validateBoundReleaseEvidence(manifest, "1.2.3", "different-revision", releaseManifest),
		"source revision mismatch",
	)
}

func TestReleaseEvidenceCheckerBindsCanonicalReleaseManifestDigest(t *testing.T) {
	directory := t.TempDir()
	manifest := writeEvidenceManifest(t, directory, "1.2.3", mandatoryReleaseEvidence)
	releaseManifest := filepath.Join(directory, "release-manifest.json")
	writeFile(t, releaseManifest, "tampered after evidence collection\n")
	assertErrorContains(
		t,
		validateBoundReleaseEvidence(manifest, "1.2.3", "revision-1", releaseManifest),
		"release manifest digest mismatch",
	)
}

func TestReleaseEvidenceCheckerBindsEveryArchiveAndExecutable(t *testing.T) {
	directory := t.TempDir()
	manifest := writeEvidenceManifest(t, directory, "1.2.3", mandatoryReleaseEvidence)
	archive := filepath.Join(directory, "bria_1.2.3_linux_amd64.tar.gz")
	bundle := "bria_1.2.3_linux_amd64"
	writeTarGZMembers(t, archive, map[string][]byte{
		bundle + "/bria":                []byte("different bria binary\n"),
		bundle + "/bria-codex-adapter":  []byte("different codex binary\n"),
		bundle + "/bria-claude-adapter": []byte("different claude binary\n"),
	})
	assertErrorContains(
		t,
		validateBoundReleaseEvidence(manifest, "1.2.3", "revision-1", filepath.Join(directory, "release-manifest.json")),
		"release artifact digest mismatch",
	)
}

func TestReleaseEvidenceCheckerRejectsStaleProducerRun(t *testing.T) {
	directory := t.TempDir()
	manifest := writeEvidenceManifest(t, directory, "1.2.3", mandatoryReleaseEvidence)
	data, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatalf("read evidence manifest: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("decode evidence manifest: %v", err)
	}
	document["generated_at"] = "2000-01-01T00:00:00Z"
	data, err = json.Marshal(document)
	if err != nil {
		t.Fatalf("encode evidence manifest: %v", err)
	}
	writeFile(t, manifest, string(data))
	assertErrorContains(
		t,
		validateBoundReleaseEvidence(manifest, "1.2.3", "revision-1", filepath.Join(directory, "release-manifest.json")),
		"stale or has invalid generated_at",
	)
}

func TestArchitectureCheckerDoesNotRelaxAwaitingCompositionDependencies(t *testing.T) {
	errors := checkGraph(graphWithEdge("internal/backup", "internal/domain"))
	assertErrorContains(t, errors, "package imports dependency outside registered boundary: internal/backup -> internal/domain")
}

func TestArchitectureCheckerRejectsPackageOverItsResponsibilityBudget(t *testing.T) {
	pkg := testPackage("internal/app")
	pkg.ProductionLines = 10_000
	assertErrorContains(t, checkGraph([]packageInfo{pkg}), "production size exceeds registered responsibility budget: internal/app")
}

func TestArchitectureCheckerCapsCompositionRootAtCurrentP1P2Budget(t *testing.T) {
	composition := testPackage("cmd/bria")
	composition.ProductionLines = 1700
	if errors := checkGraph([]packageInfo{composition}); len(errors) != 0 {
		t.Fatalf("checkGraph() errors = %v, want none", errors)
	}
	composition.ProductionLines++
	assertErrorContains(t, checkGraph([]packageInfo{composition}), "production size exceeds registered responsibility budget: cmd/bria")
}

func TestArchitectureCheckerCapsCoherentCustodyResponsibilities(t *testing.T) {
	for _, test := range []struct {
		path  string
		limit int
	}{
		{path: "internal/authflow", limit: 1500},
		{path: "internal/mediaflow", limit: 350},
		{path: "internal/messagejournal", limit: 1400},
		{path: "internal/sessionsupervisor", limit: 450},
		{path: "internal/telegramflow", limit: 2000},
		{path: "internal/telegrampipeline", limit: 1500},
	} {
		pkg := testPackage(test.path)
		pkg.ProductionLines = test.limit
		if errors := checkGraph([]packageInfo{pkg}); len(errors) != 0 {
			t.Fatalf("%s at limit errors = %v, want none", test.path, errors)
		}
		pkg.ProductionLines++
		assertErrorContains(t, checkGraph([]packageInfo{pkg}), "production size exceeds registered responsibility budget: "+test.path)
	}
}

func TestArchitectureCheckerIgnoresExternalTestSelfImportForProductionGraph(t *testing.T) {
	pkg := testPackage("internal/backup")
	pkg.XTestImports = []string{pkg.ImportPath}
	if errors := checkGraph([]packageInfo{pkg}); len(errors) != 0 {
		t.Fatalf("checkGraph() errors = %v, want none", errors)
	}
}

func TestArchitectureCheckerRejectsNonCommandImportingCommand(t *testing.T) {
	errors := checkGraph(graphWithEdge("internal/feature", "cmd/bria"))
	assertErrorContains(t, errors, "non-cmd package imports cmd")
}

func TestArchitectureCheckerRejectsDomainAndAppBoundaryViolations(t *testing.T) {
	assertErrorContains(
		t,
		checkGraph(graphWithEdge("internal/domain", "internal/app")),
		"domain imports another Bria package",
	)
	assertErrorContains(
		t,
		checkGraph(graphWithEdge("internal/app", "internal/storage")),
		"app imports infrastructure",
	)
}

func TestArchitectureCheckerRejectsProviderCrossImports(t *testing.T) {
	for _, edge := range [][2]string{
		{"internal/provider/codex", "internal/provider/claude"},
		{"internal/provider/claude", "internal/provider/codex"},
	} {
		assertErrorContains(t, checkGraph(graphWithEdge(edge[0], edge[1])), "provider adapters import each other")
	}
}

func TestArchitectureCheckerRejectsTelegramAppConcreteAdapters(t *testing.T) {
	for _, target := range []string{
		"internal/telegram",
		"internal/storage",
		"internal/sessionruntime",
		"internal/executor",
		"internal/provider/codex",
	} {
		assertErrorContains(
			t,
			checkGraph(graphWithEdge("internal/telegramapp", target)),
			"telegram app imports concrete adapter: internal/telegramapp -> "+target,
		)
	}
}

func TestArchitectureCheckerRejectsTelegramUITransportOrControl(t *testing.T) {
	for _, target := range []string{
		"internal/telegram",
		"internal/telegramapp",
		"internal/storage",
		"internal/sessionruntime",
		"internal/executor",
		"internal/provider/claude",
	} {
		assertErrorContains(
			t,
			checkGraph(graphWithEdge("internal/telegramui", target)),
			"telegram UI imports transport or control package: internal/telegramui -> "+target,
		)
	}
}

func TestArchitectureCheckerRejectsSessionIDInfrastructure(t *testing.T) {
	for _, target := range []string{
		"internal/telegram",
		"internal/telegramapp",
		"internal/telegramui",
		"internal/storage",
		"internal/sessionruntime",
		"internal/executor",
		"internal/provider/codex",
		"cmd/bria",
	} {
		assertErrorContains(
			t,
			checkGraph(graphWithEdge("internal/sessionid", target)),
			"session id source imports infrastructure: internal/sessionid -> "+target,
		)
	}
}

func TestArchitectureCheckerRejectsConfigImportsOutsideDomain(t *testing.T) {
	for _, target := range []string{
		"internal/storage",
		"internal/telegram",
		"internal/provider/codex",
	} {
		assertErrorContains(
			t,
			checkGraph(graphWithEdge("internal/config", target)),
			"config imports package outside domain: internal/config -> "+target,
		)
	}
}

func TestArchitectureCheckerRejectsWorkdirConcreteDependencies(t *testing.T) {
	for _, target := range []string{
		"internal/config",
		"internal/storage",
		"internal/telegram",
		"internal/provider/claude",
	} {
		assertErrorContains(
			t,
			checkGraph(graphWithEdge("internal/workdir", target)),
			"workdir validator imports concrete component: internal/workdir -> "+target,
		)
	}
}

func TestArchitectureCheckerRejectsTelegramProductDependencies(t *testing.T) {
	for _, target := range []string{
		"internal/domain",
		"internal/app",
		"internal/telegramapp",
		"internal/storage",
	} {
		assertErrorContains(
			t,
			checkGraph(graphWithEdge("internal/telegram", target)),
			"Telegram transport imports Bria product package: internal/telegram -> "+target,
		)
	}
}

func TestArchitectureCheckerRejectsTelegramUIAdditionalConcreteDependencies(t *testing.T) {
	for _, target := range []string{
		"internal/config",
		"internal/workdir",
		"internal/nodelink",
	} {
		assertErrorContains(
			t,
			checkGraph(graphWithEdge("internal/telegramui", target)),
			"telegram UI imports transport or control package: internal/telegramui -> "+target,
		)
	}
}

func TestArchitectureCheckerRejectsProviderAdapterConcreteDependencies(t *testing.T) {
	for _, source := range []string{"internal/provider/codex", "internal/provider/claude"} {
		for _, target := range []string{
			"internal/config",
			"internal/workdir",
			"internal/telegram",
			"internal/storage",
			"internal/sessionruntime",
		} {
			assertErrorContains(
				t,
				checkGraph(graphWithEdge(source, target)),
				"provider adapter imports concrete component: "+source+" -> "+target,
			)
		}
	}
}

func TestArchitectureCheckerAllowsImplementedProvidersToUseSharedAuthorizationFlow(t *testing.T) {
	for _, provider := range []string{"internal/provider/codex", "internal/provider/claude"} {
		packages := []packageInfo{
			testPackage(provider, "internal/authflow"),
			testPackage("internal/authflow"),
		}
		if errors := checkGraph(packages); len(errors) != 0 {
			t.Fatalf("%s authflow errors = %v, want none", provider, errors)
		}
	}
}

func TestArchitectureCheckerRegistersTelegramProviderAuthorizationComposition(t *testing.T) {
	packages := []packageInfo{
		testPackage("internal/authcomposition", "internal/authflow", "internal/domain", "internal/provider/claude", "internal/provider/codex", "internal/telegram", "internal/telegramcontroller"),
		testPackage("internal/authflow"),
		testPackage("internal/domain"),
		testPackage("internal/provider/claude"),
		testPackage("internal/provider/codex"),
		testPackage("internal/telegram"),
		testPackage("internal/telegramcontroller", "internal/domain"),
	}
	if errors := checkGraph(packages); len(errors) != 0 {
		t.Fatalf("checkGraph() errors = %v, want none", errors)
	}
	assertErrorContains(
		t,
		checkGraph(graphWithEdge("internal/provider/codex", "internal/authcomposition")),
		"package imports dependency outside registered boundary: internal/provider/codex -> internal/authcomposition",
	)
}

func TestArchitectureCheckerRegistersLandedP3InteractionComposition(t *testing.T) {
	interactionComposition := testPackage(
		"internal/interactioncomposition",
		"internal/interactionflow", "internal/telegram", "internal/telegrambridge", "internal/telegramflow", "internal/telegrampipeline",
	)
	interactionComposition.ProductionLines = 150
	packages := []packageInfo{
		testPackage("cmd/bria", "internal/singlemachinecomposition"),
		testPackage("internal/singlemachinecomposition", "internal/authcomposition", "internal/interactioncomposition"),
		testPackage("internal/authcomposition"),
		interactionComposition,
		testPackage("internal/interactionflow"),
		testPackage("internal/telegram"),
		testPackage("internal/telegrambridge"),
		testPackage("internal/telegramflow"),
		testPackage("internal/telegrampipeline"),
	}
	if errors := checkGraph(packages); len(errors) != 0 {
		t.Fatalf("checkGraph() errors = %v, want none", errors)
	}
}

func TestArchitectureCheckerRegistersLandedRecoveryComposition(t *testing.T) {
	recovery := testPackage("internal/recoverycomposition", "internal/domain", "internal/sessionruntime", "internal/sessionsupervisor")
	recovery.ProductionLines = 80
	supervision := testPackage("internal/supervisioncomposition", "internal/app", "internal/domain", "internal/sessionsupervisor")
	supervision.ProductionLines = 350
	packages := []packageInfo{
		testPackage("cmd/bria", "internal/singlemachinecomposition"),
		testPackage("internal/singlemachinecomposition", "internal/recoverycomposition", "internal/supervisioncomposition"),
		recovery,
		supervision,
		testPackage("internal/app", "internal/domain"),
		testPackage("internal/domain"),
		testPackage("internal/sessionruntime", "internal/domain"),
		testPackage("internal/sessionsupervisor", "internal/app", "internal/domain"),
	}
	if errors := checkGraph(packages); len(errors) != 0 {
		t.Fatalf("checkGraph() errors = %v, want none", errors)
	}
	assertErrorContains(
		t,
		checkGraph(graphWithEdge("internal/sessionsupervisor", "internal/recoverycomposition")),
		"package imports dependency outside registered boundary: internal/sessionsupervisor -> internal/recoverycomposition",
	)
}

func TestArchitectureCheckerAllowsOnlyCodexProviderToImplementSessionDiscovery(t *testing.T) {
	codex := []packageInfo{
		testPackage("internal/provider/codex", "internal/sessiondiscovery"),
		testPackage("internal/sessiondiscovery", "internal/domain"),
		testPackage("internal/domain"),
	}
	if errors := checkGraph(codex); len(errors) != 0 {
		t.Fatalf("Codex sessiondiscovery errors = %v, want none", errors)
	}
	assertErrorContains(
		t,
		checkGraph(graphWithEdge("internal/provider/claude", "internal/sessiondiscovery")),
		"provider adapter imports concrete component: internal/provider/claude -> internal/sessiondiscovery",
	)
}

func TestArchitectureCheckerAllowsProvidersToImplementAcceptedTurnReconciliation(t *testing.T) {
	for _, provider := range []string{"internal/provider/codex", "internal/provider/claude"} {
		packages := []packageInfo{
			testPackage(provider, "internal/sessionsupervisor"),
			testPackage("internal/sessionsupervisor", "internal/app", "internal/domain"),
			testPackage("internal/app", "internal/domain"),
			testPackage("internal/domain"),
		}
		if provider == "internal/provider/codex" {
			packages[0].ProductionLines = 3000
		}
		if errors := checkGraph(packages); len(errors) != 0 {
			t.Fatalf("%s sessionsupervisor errors = %v, want none", provider, errors)
		}
	}
	assertErrorContains(
		t,
		checkGraph(graphWithEdge("internal/sessionsupervisor", "internal/provider/codex")),
		"package imports dependency outside registered boundary: internal/sessionsupervisor -> internal/provider/codex",
	)
}

func TestArchitectureCheckerRejectsProcessInfrastructureProductDependencies(t *testing.T) {
	for _, source := range []string{
		"internal/processenv",
		"internal/processgroup",
		"internal/instancelock",
	} {
		for _, target := range []string{
			"internal/domain",
			"internal/app",
			"internal/sessionruntime",
			"internal/telegram",
		} {
			assertErrorContains(
				t,
				checkGraph(graphWithEdge(source, target)),
				"process infrastructure imports Bria package: "+source+" -> "+target,
			)
		}
	}
}

func TestArchitectureCheckerRejectsRuntimeFactoryDependenciesOutsideComposition(t *testing.T) {
	for _, target := range []string{
		"internal/storage",
		"internal/telegram",
		"internal/telegramcontroller",
		"internal/provider/codex",
		"internal/processgroup",
	} {
		assertErrorContains(
			t,
			checkGraph(graphWithEdge("internal/runtimefactory", target)),
			"runtime factory imports package outside its composition boundary: internal/runtimefactory -> "+target,
		)
	}
}

func TestArchitectureCheckerAllowsOnlyRuntimeFactoryExternalProviderAcceptanceImports(t *testing.T) {
	for _, provider := range []string{"internal/provider/codex", "internal/provider/claude"} {
		if errors := checkGraph(graphWithTestEdge("internal/runtimefactory", provider, true)); len(errors) != 0 {
			t.Fatalf("external %s acceptance errors = %v, want none", provider, errors)
		}
		assertErrorContains(
			t,
			checkGraph(graphWithTestEdge("internal/runtimefactory", provider, false)),
			"runtime factory imports package outside its composition boundary",
		)
	}
}

func TestArchitectureCheckerRejectsProviderRuntimeTelegramAndConcreteDependencies(t *testing.T) {
	for _, target := range []string{
		"internal/config",
		"internal/storage",
		"internal/telegram",
		"internal/telegrambridge",
		"internal/telegramcontroller",
		"internal/telegramnotify",
		"internal/telegramui",
		"internal/provider/codex",
	} {
		assertErrorContains(
			t,
			checkGraph(graphWithEdge("internal/sessionruntime", target)),
			"session runtime imports concrete product or Telegram package: internal/sessionruntime -> "+target,
		)
	}
}

func TestArchitectureCheckerRejectsTelegramControllerConcreteDependencies(t *testing.T) {
	for _, target := range []string{
		"internal/config",
		"internal/storage",
		"internal/telegram",
		"internal/telegrambridge",
		"internal/telegramnotify",
		"internal/telegramui",
		"internal/provider/codex",
		"internal/provider/claude",
		"internal/runtimefactory",
	} {
		assertErrorContains(
			t,
			checkGraph(graphWithEdge("internal/telegramcontroller", target)),
			"Telegram controller imports concrete adapter: internal/telegramcontroller -> "+target,
		)
	}
}

func TestArchitectureCheckerRegistersProviderNeutralTurnProcessingSplit(t *testing.T) {
	turnProcessing := testPackage("internal/turnprocessing", "internal/domain", "internal/sessionruntime")
	turnProcessing.ProductionLines = 250
	packages := []packageInfo{
		testPackage("internal/telegramcontroller", "internal/app", "internal/coordinator", "internal/domain", "internal/sessionruntime", "internal/turnprocessing"),
		turnProcessing,
		testPackage("internal/app", "internal/domain"),
		testPackage("internal/coordinator"),
		testPackage("internal/domain"),
		testPackage("internal/sessionruntime", "internal/domain"),
	}
	if errors := checkGraph(packages); len(errors) != 0 {
		t.Fatalf("checkGraph() errors = %v, want none", errors)
	}
	assertErrorContains(
		t,
		checkGraph(graphWithEdge("internal/turnprocessing", "internal/telegramcontroller")),
		"package imports dependency outside registered boundary: internal/turnprocessing -> internal/telegramcontroller",
	)
}

func TestArchitectureCheckerRejectsTelegramNotifierDependenciesOutsideDelivery(t *testing.T) {
	for _, target := range []string{
		"internal/app",
		"internal/config",
		"internal/coordinator",
		"internal/storage",
		"internal/sessionruntime",
		"internal/provider/claude",
	} {
		assertErrorContains(
			t,
			checkGraph(graphWithEdge("internal/telegramnotify", target)),
			"Telegram notifier imports package outside delivery boundary: internal/telegramnotify -> "+target,
		)
	}
}

func TestArchitectureCheckerRejectsTelegramBridgeDependenciesOutsideTransportAdaptation(t *testing.T) {
	for _, target := range []string{
		"internal/app",
		"internal/domain",
		"internal/storage",
		"internal/sessionruntime",
		"internal/telegramcontroller",
		"internal/telegramnotify",
		"internal/provider/codex",
	} {
		assertErrorContains(
			t,
			checkGraph(graphWithEdge("internal/telegrambridge", target)),
			"Telegram bridge imports package outside transport adaptation: internal/telegrambridge -> "+target,
		)
	}
}

func TestArchitectureCheckerKeepsCoordinatorTransportNeutral(t *testing.T) {
	for _, target := range []string{
		"internal/app",
		"internal/domain",
		"internal/storage",
		"internal/telegram",
		"internal/telegramcontroller",
	} {
		assertErrorContains(
			t,
			checkGraph(graphWithEdge("internal/coordinator", target)),
			"coordinator imports product or transport package: internal/coordinator -> "+target,
		)
	}
}

func TestArchitectureCheckerAllowsProviderProcessGroupAndLandedOneWayDependencies(t *testing.T) {
	packages := []packageInfo{
		testPackage("internal/processenv"),
		testPackage("internal/processgroup"),
		testPackage("internal/instancelock"),
		testPackage("internal/runtimefactory", "internal/app", "internal/config", "internal/domain", "internal/processenv", "internal/sessionruntime"),
		testPackage("internal/sessionruntime", "internal/app", "internal/domain", "internal/processgroup"),
		testPackage("internal/provider/codex", "internal/processgroup"),
		testPackage("internal/provider/claude", "internal/runtimeprotocol"),
		testPackage("internal/telegramcontroller", "internal/app", "internal/coordinator", "internal/domain", "internal/sessionruntime"),
		testPackage("internal/telegrambridge", "internal/callbacktoken", "internal/coordinator", "internal/telegram", "internal/telegramui"),
		testPackage("internal/telegramnotify", "internal/domain", "internal/telegram", "internal/telegramcontroller", "internal/telegramui"),
		testPackage("internal/app", "internal/domain"),
		testPackage("internal/config", "internal/domain"),
		testPackage("internal/domain"),
		testPackage("internal/callbacktoken"),
		testPackage("internal/coordinator"),
		testPackage("internal/telegram"),
		testPackage("internal/telegramui", "internal/app", "internal/domain"),
	}
	if errors := checkGraph(packages); len(errors) != 0 {
		t.Fatalf("checkGraph() errors = %v, want none", errors)
	}
	for _, edge := range [][2]string{
		{"internal/provider/codex", "internal/app"},
		{"internal/provider/claude", "internal/app"},
		{"internal/provider/claude", "internal/processgroup"},
	} {
		assertErrorContains(
			t,
			checkGraph(graphWithEdge(edge[0], edge[1])),
			"package imports dependency outside registered boundary: "+edge[0]+" -> "+edge[1],
		)
	}
}

func TestArchitectureCheckerRejectsNodeLinkStorageBypass(t *testing.T) {
	errors := checkGraph(graphWithEdge("internal/nodelink", "internal/storage"))
	assertErrorContains(t, errors, "node link bypasses application storage boundary")
}

func TestArchitectureCheckerAllowsExecutorToAdaptAuthenticatedNodeLink(t *testing.T) {
	packages := []packageInfo{
		testPackage("internal/executor", "internal/computer", "internal/domain", "internal/nodelink"),
		testPackage("internal/computer", "internal/domain"),
		testPackage("internal/domain"),
		testPackage("internal/nodelink", "internal/computer", "internal/domain"),
	}
	if errors := checkGraph(packages); len(errors) != 0 {
		t.Fatalf("checkGraph() errors = %v, want none", errors)
	}
}

func TestArchitectureCheckerRegistersPairingOnlyNodeBootstrap(t *testing.T) {
	packages := []packageInfo{
		testPackage("internal/nodebootstrap", "internal/computer", "internal/nodelink"),
		testPackage("internal/computer", "internal/domain"),
		testPackage("internal/domain"),
		testPackage("internal/nodelink", "internal/computer", "internal/domain"),
	}
	if errors := checkGraph(packages); len(errors) != 0 {
		t.Fatalf("checkGraph() errors = %v, want none", errors)
	}
	assertErrorContains(
		t,
		checkGraph(graphWithEdge("internal/nodebootstrap", "internal/telegram")),
		"package imports dependency outside registered boundary: internal/nodebootstrap -> internal/telegram",
	)
}

func TestArchitectureCheckerRegistersLandedBackupRuntimeAndUpdateFlow(t *testing.T) {
	packages := []packageInfo{
		testPackage("internal/backupruntime", "internal/backup", "internal/backupflow"),
		testPackage("internal/backupflow", "internal/backup"),
		testPackage("internal/backup"),
		testPackage("internal/updateflow", "internal/update"),
		testPackage("internal/update"),
	}
	if errors := checkGraph(packages); len(errors) != 0 {
		t.Fatalf("checkGraph() errors = %v, want none", errors)
	}
}

func TestArchitectureCheckerRegistersStableUpdateSourceRuntime(t *testing.T) {
	updateRuntime := testPackage("internal/updateruntime", "internal/update", "internal/updateflow", "internal/updateinstall")
	updateRuntime.ProductionLines = 850
	updateInstall := testPackage("internal/updateinstall", "internal/update", "internal/updateflow")
	updateInstall.ProductionLines = 780
	packages := []packageInfo{
		updateRuntime,
		updateInstall,
		testPackage("internal/update"),
		testPackage("internal/updateflow", "internal/update"),
	}
	if errors := checkGraph(packages); len(errors) != 0 {
		t.Fatalf("checkGraph() errors = %v, want none", errors)
	}
}

func TestArchitectureCheckerRegistersSemanticBackupSource(t *testing.T) {
	packages := []packageInfo{
		testPackage("internal/backupsource", "internal/backupruntime", "internal/computer", "internal/domain", "internal/messagejournal", "internal/settings"),
		testPackage("internal/backupruntime", "internal/backup", "internal/backupflow"),
		testPackage("internal/backup"),
		testPackage("internal/backupflow", "internal/backup"),
		testPackage("internal/computer", "internal/domain"),
		testPackage("internal/domain"),
		testPackage("internal/messagejournal"),
		testPackage("internal/settings"),
	}
	if errors := checkGraph(packages); len(errors) != 0 {
		t.Fatalf("checkGraph() errors = %v, want none", errors)
	}
}

func TestArchitectureCheckerRegistersStableMediaProductionAdapters(t *testing.T) {
	packages := []packageInfo{
		testPackage("internal/mediaproduction", "internal/files", "internal/mediaflow", "internal/speech/parakeet"),
		testPackage("internal/files"),
		testPackage("internal/mediaflow", "internal/files"),
		testPackage("internal/speech/parakeet", "internal/speech"),
		testPackage("internal/speech"),
	}
	if errors := checkGraph(packages); len(errors) != 0 {
		t.Fatalf("checkGraph() errors = %v, want none", errors)
	}
}

func TestArchitectureCheckerRegistersBoundedRuntimeEventScreen(t *testing.T) {
	packages := []packageInfo{
		testPackage("internal/screen", "internal/domain", "internal/sessionruntime"),
		testPackage("internal/domain"),
		testPackage("internal/sessionruntime", "internal/domain", "internal/runtimeprotocol"),
		testPackage("internal/runtimeprotocol"),
	}
	if errors := checkGraph(packages); len(errors) != 0 {
		t.Fatalf("checkGraph() errors = %v, want none", errors)
	}
}

func TestArchitectureCheckerRegistersStableArtifactProductionDelivery(t *testing.T) {
	artifactProduction := testPackage("internal/artifactproduction", "internal/artifactdelivery", "internal/files", "internal/telegram")
	artifactProduction.ProductionLines = 990
	packages := []packageInfo{
		artifactProduction,
		testPackage("internal/artifactdelivery", "internal/files"),
		testPackage("internal/files"),
		testPackage("internal/telegram"),
	}
	if errors := checkGraph(packages); len(errors) != 0 {
		t.Fatalf("checkGraph() errors = %v, want none", errors)
	}
}

func TestArchitectureCheckerRegistersFrozenProductionPackagePolicies(t *testing.T) {
	tests := []struct {
		path           string
		responsibility string
		imports        []string
		limit          int
		evidence       string
	}{
		{
			path:           "internal/artifactcomposition",
			responsibility: "route terminal observations into durable artifact delivery",
			imports:        []string{"internal/artifactproduction", "internal/turnprocessing"},
			limit:          150,
		},
		{
			path:           "internal/inputcomposition",
			responsibility: "connect production media preparation to structured durable attachment custody",
			imports:        []string{"internal/mediaproduction", "internal/turnprocessing"},
			limit:          150,
		},
		{
			path:           "internal/screenproduction",
			responsibility: "project typed provider events into virtual screen and optional Telegram media",
			imports:        []string{"internal/screen", "internal/settings", "internal/telegram", "internal/turnprocessing"},
			limit:          200,
		},
		{
			path:           "internal/containerpreflight",
			responsibility: "verify immutable mounted provider artifacts before a Docker role starts",
			imports:        []string{"internal/config"},
			limit:          450,
			evidence:       "platform_docker_executor",
		},
		{
			path:           "internal/recoveryruntime",
			responsibility: "run bounded provider adapters as read-only accepted-turn history readers",
			imports:        []string{"internal/claudestore", "internal/domain", "internal/processgroup", "internal/runtimeprotocol", "internal/sessionruntime"},
			limit:          400,
		},
		{
			path:           "internal/updatecomposition",
			responsibility: "bind explicit update triggers to the durable signed-release flow",
			imports:        []string{"internal/update", "internal/updateflow", "internal/updateinstall"},
			limit:          400,
			evidence:       "update_and_forced_rollback",
		},
	}

	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			policy, ok := packagePolicies[test.path]
			if !ok {
				t.Fatalf("packagePolicies[%q] is missing", test.path)
			}
			if policy.responsibility != test.responsibility {
				t.Fatalf("responsibility = %q, want %q", policy.responsibility, test.responsibility)
			}
			if policy.maxProductionLines != test.limit {
				t.Fatalf("maxProductionLines = %d, want %d", policy.maxProductionLines, test.limit)
			}
			if strings.Join(policy.allowedImports, "\x00") != strings.Join(test.imports, "\x00") {
				t.Fatalf("allowedImports = %v, want %v", policy.allowedImports, test.imports)
			}
			if policy.externalOnlyEvidence != test.evidence {
				t.Fatalf("externalOnlyEvidence = %q, want %q", policy.externalOnlyEvidence, test.evidence)
			}
		})
	}
}

func TestArchitectureCheckerFrozenProductionPoliciesEnforceEdgesAndReleaseBlockers(t *testing.T) {
	tests := []struct {
		path     string
		imports  []string
		evidence string
	}{
		{path: "internal/artifactcomposition", imports: []string{"internal/artifactproduction", "internal/turnprocessing"}},
		{path: "internal/inputcomposition", imports: []string{"internal/mediaproduction", "internal/turnprocessing"}},
		{path: "internal/screenproduction", imports: []string{"internal/screen", "internal/settings", "internal/telegram", "internal/turnprocessing"}},
		{path: "internal/containerpreflight", imports: []string{"internal/config"}, evidence: "platform_docker_executor"},
		{path: "internal/recoveryruntime", imports: []string{"internal/claudestore", "internal/domain", "internal/processgroup", "internal/runtimeprotocol", "internal/sessionruntime"}},
		{path: "internal/updatecomposition", imports: []string{"internal/update", "internal/updateflow", "internal/updateinstall"}, evidence: "update_and_forced_rollback"},
	}

	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			allowedGraph := []packageInfo{testPackage(test.path, test.imports...)}
			for _, imported := range test.imports {
				allowedGraph = append(allowedGraph, testPackage(imported))
			}
			if errors := checkGraph(allowedGraph); len(errors) != 0 {
				t.Fatalf("approved imports rejected: %v", errors)
			}

			assertErrorContains(t, checkGraph(graphWithEdge(test.path, "internal/safelog")), "package imports dependency outside registered boundary: "+test.path+" -> internal/safelog")

			blockers := externalOnlyReleaseBlockers([]packageInfo{testPackage(test.path)})
			if test.evidence == "" {
				if len(blockers) != 0 {
					t.Fatalf("external-only blockers = %v, want none", blockers)
				}
				return
			}
			want := "external-only product package blocks release: " + test.path + " (required evidence category " + test.evidence + ")"
			assertErrorContains(t, blockers, want)
		})
	}
}

func TestArchitectureCheckerRegistersSecondFrozenProductionWave(t *testing.T) {
	tests := []struct {
		path           string
		responsibility string
		imports        []string
		limit          int
		evidence       string
	}{
		{
			path:           "internal/backupcomposition",
			responsibility: "compose manual verified backup and restore operations",
			imports:        []string{"internal/backupflow", "internal/backupruntime", "internal/backupsource", "internal/domain"},
			limit:          600,
			evidence:       "backup_restore",
		},
		{
			path:           "internal/multinodecomposition",
			responsibility: "compose durable multi-computer coordinator roles and manual cutover",
			imports:        []string{"internal/coordinatortransfer", "internal/nodebootstrap", "internal/nodelink"},
			limit:          700,
			evidence:       "concurrency_and_recovery",
		},
		{
			path:           "internal/claudestore",
			responsibility: "read and verify Claude provider-owned transcript records",
			imports:        nil,
			limit:          600,
			evidence:       "",
		},
		{
			path:           "internal/archiveimport",
			responsibility: "validate and merge externally discovered archived sessions",
			imports:        []string{"internal/domain"},
			limit:          200,
			evidence:       "",
		},
		{
			path:           "internal/sessioncatalog",
			responsibility: "persist one origin-neutral archive of discovered provider sessions",
			imports:        []string{"internal/domain", "internal/sessiondiscovery"},
			limit:          500,
			evidence:       "telegram_codex_claude_e2e",
		},
		{
			path:           "internal/sessiondiscovery/claudeindex",
			responsibility: "adapt Claude transcript summaries to session discovery",
			imports:        []string{"internal/claudestore", "internal/domain", "internal/sessiondiscovery"},
			limit:          200,
			evidence:       "telegram_codex_claude_e2e",
		},
	}

	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			policy, ok := packagePolicies[test.path]
			if !ok {
				t.Fatalf("packagePolicies[%q] is missing", test.path)
			}
			if policy.responsibility != test.responsibility {
				t.Fatalf("responsibility = %q, want %q", policy.responsibility, test.responsibility)
			}
			if policy.maxProductionLines != test.limit {
				t.Fatalf("maxProductionLines = %d, want %d", policy.maxProductionLines, test.limit)
			}
			if strings.Join(policy.allowedImports, "\x00") != strings.Join(test.imports, "\x00") {
				t.Fatalf("allowedImports = %v, want %v", policy.allowedImports, test.imports)
			}
			if policy.externalOnlyEvidence != test.evidence {
				t.Fatalf("externalOnlyEvidence = %q, want %q", policy.externalOnlyEvidence, test.evidence)
			}

			approvedGraph := []packageInfo{testPackage(test.path, test.imports...)}
			for _, imported := range test.imports {
				approvedGraph = append(approvedGraph, testPackage(imported))
			}
			if errors := checkGraph(approvedGraph); len(errors) != 0 {
				t.Fatalf("approved imports rejected: %v", errors)
			}
			assertErrorContains(t, checkGraph(graphWithEdge(test.path, "internal/safelog")), "package imports dependency outside registered boundary: "+test.path+" -> internal/safelog")

			blockers := externalOnlyReleaseBlockers([]packageInfo{testPackage(test.path)})
			if test.evidence == "" {
				if len(blockers) != 0 {
					t.Fatalf("external-only blockers = %v, want none", blockers)
				}
				return
			}
			assertErrorContains(t, blockers, "external-only product package blocks release: "+test.path+" (required evidence category "+test.evidence+")")
		})
	}
}

func TestArchitectureCheckerExtendsStorageAndRecoveryRuntimeEdges(t *testing.T) {
	tests := []struct {
		path    string
		imports []string
	}{
		{
			path:    "internal/storage",
			imports: []string{"internal/archiveimport", "internal/coordinator", "internal/domain", "internal/telegramstate"},
		},
		{
			path:    "internal/recoveryruntime",
			imports: []string{"internal/claudestore", "internal/domain", "internal/processgroup", "internal/runtimeprotocol", "internal/sessionruntime"},
		},
	}

	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			policy := packagePolicies[test.path]
			if strings.Join(policy.allowedImports, "\x00") != strings.Join(test.imports, "\x00") {
				t.Fatalf("allowedImports = %v, want %v", policy.allowedImports, test.imports)
			}
		})
	}
}

func TestArchitectureCheckerRegistersSettingsAndProviderInputPolicies(t *testing.T) {
	tests := []struct {
		path           string
		responsibility string
		imports        []string
		limit          int
		evidence       string
	}{
		{
			path:           "internal/providerinputcomposition",
			responsibility: "resolve durable attachment custody at the provider boundary without flattening local paths into prompt text",
			imports:        []string{"internal/domain", "internal/sessionruntime", "internal/turnprocessing"},
			limit:          250,
			evidence:       "",
		},
		{
			path:           "internal/settingsport",
			responsibility: "define the storage-neutral preferences boundary used by Telegram control surfaces",
			imports:        []string{"internal/domain"},
			limit:          100,
		},
		{
			path:           "internal/settingscomposition",
			responsibility: "compose neutral Telegram settings ports with canonical local settings and configuration stores",
			imports:        []string{"internal/config", "internal/domain", "internal/settings", "internal/settingsport"},
			limit:          200,
		},
		{
			path:           "internal/telegramsettings",
			responsibility: "render and apply Telegram settings surfaces through neutral preferences ports",
			imports:        []string{"internal/domain", "internal/settingsport"},
			limit:          200,
		},
	}

	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			policy, ok := packagePolicies[test.path]
			if !ok {
				t.Fatalf("packagePolicies[%q] is missing", test.path)
			}
			if policy.responsibility != test.responsibility {
				t.Fatalf("responsibility = %q, want %q", policy.responsibility, test.responsibility)
			}
			if policy.maxProductionLines != test.limit {
				t.Fatalf("maxProductionLines = %d, want %d", policy.maxProductionLines, test.limit)
			}
			if strings.Join(policy.allowedImports, "\x00") != strings.Join(test.imports, "\x00") {
				t.Fatalf("allowedImports = %v, want %v", policy.allowedImports, test.imports)
			}
			if policy.externalOnlyEvidence != test.evidence {
				t.Fatalf("externalOnlyEvidence = %q, want %q", policy.externalOnlyEvidence, test.evidence)
			}

			approvedGraph := []packageInfo{testPackage(test.path, test.imports...)}
			for _, imported := range test.imports {
				approvedGraph = append(approvedGraph, testPackage(imported))
			}
			if errors := checkGraph(approvedGraph); len(errors) != 0 {
				t.Fatalf("approved imports rejected: %v", errors)
			}
			assertErrorContains(t, checkGraph(graphWithEdge(test.path, "internal/safelog")), "package imports dependency outside registered boundary: "+test.path+" -> internal/safelog")

			blockers := externalOnlyReleaseBlockers([]packageInfo{testPackage(test.path)})
			if test.evidence == "" {
				if len(blockers) != 0 {
					t.Fatalf("external-only blockers = %v, want none", blockers)
				}
				return
			}
			assertErrorContains(t, blockers, "external-only product package blocks release: "+test.path+" (required evidence category "+test.evidence+")")
		})
	}
}

func TestArchitectureCheckerAllowsOnlySettingsBoundaryEdges(t *testing.T) {
	tests := []struct {
		source string
		target string
	}{
		{source: "internal/settings", target: "internal/settingsport"},
		{source: "internal/telegramcontroller", target: "internal/settingsport"},
		{source: "internal/telegramcontroller", target: "internal/telegramsettings"},
	}

	for _, test := range tests {
		t.Run(test.source+" -> "+test.target, func(t *testing.T) {
			if errors := checkGraph(graphWithEdge(test.source, test.target)); len(errors) != 0 {
				t.Fatalf("settings boundary edge rejected: %v", errors)
			}
		})
	}
}

func TestArchitectureCheckerRegistersFrozenRecoveryPolicies(t *testing.T) {
	tests := []struct {
		path           string
		responsibility string
		imports        []string
		limit          int
		evidence       string
	}{
		{
			path:           "internal/coordinator/recoverycontrol",
			responsibility: "bind an unknown operation to its separate signed recovery prompt",
			limit:          50,
		},
		{
			path:           "internal/telegramrecovery/statusrecovery",
			responsibility: "define identity for resolving one exact unknown Telegram status write",
			imports:        []string{"internal/domain", "internal/telegramstate", "internal/telegramui"},
			limit:          100,
		},
		{
			path:           "internal/telegramrecoverycomposition",
			responsibility: "resolve signed owner recovery clicks against exact durable Telegram operations and request a fresh projection",
			imports: []string{
				"internal/coordinator", "internal/telegrambridge", "internal/telegramflow", "internal/telegramrecovery/statusrecovery",
				"internal/telegrampipeline", "internal/telegramstate", "internal/telegramui",
			},
			limit: 400,
		},
	}

	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			policy, ok := packagePolicies[test.path]
			if !ok {
				t.Fatalf("packagePolicies[%q] is missing", test.path)
			}
			if policy.responsibility != test.responsibility {
				t.Fatalf("responsibility = %q, want %q", policy.responsibility, test.responsibility)
			}
			if policy.maxProductionLines != test.limit {
				t.Fatalf("maxProductionLines = %d, want %d", policy.maxProductionLines, test.limit)
			}
			if strings.Join(policy.allowedImports, "\x00") != strings.Join(test.imports, "\x00") {
				t.Fatalf("allowedImports = %v, want %v", policy.allowedImports, test.imports)
			}
			if policy.externalOnlyEvidence != test.evidence {
				t.Fatalf("externalOnlyEvidence = %q, want %q", policy.externalOnlyEvidence, test.evidence)
			}

			approvedGraph := []packageInfo{testPackage(test.path, test.imports...)}
			for _, imported := range test.imports {
				approvedGraph = append(approvedGraph, testPackage(imported))
			}
			if errors := checkGraph(approvedGraph); len(errors) != 0 {
				t.Fatalf("approved imports rejected: %v", errors)
			}
			assertErrorContains(t, checkGraph(graphWithEdge(test.path, "internal/safelog")), "package imports dependency outside registered boundary: "+test.path+" -> internal/safelog")

			blockers := externalOnlyReleaseBlockers([]packageInfo{testPackage(test.path)})
			if test.evidence == "" {
				if len(blockers) != 0 {
					t.Fatalf("external-only blockers = %v, want none", blockers)
				}
				return
			}
			assertErrorContains(t, blockers, "external-only product package blocks release: "+test.path+" (required evidence category "+test.evidence+")")
		})
	}
}

func TestArchitectureCheckerAllowsOnlyFrozenRecoveryEdges(t *testing.T) {
	tests := []struct {
		source string
		target string
	}{
		{source: "internal/coordinator", target: "internal/coordinator/recoverycontrol"},
		{source: "internal/telegrambridge", target: "internal/telegramrecovery/statusrecovery"},
		{source: "internal/telegramflow", target: "internal/telegramrecovery/statusrecovery"},
		{source: "internal/telegrampipeline", target: "internal/telegramrecovery/statusrecovery"},
	}

	for _, test := range tests {
		t.Run(test.source+" -> "+test.target, func(t *testing.T) {
			if errors := checkGraph(graphWithEdge(test.source, test.target)); len(errors) != 0 {
				t.Fatalf("recovery edge rejected: %v", errors)
			}
		})
	}
}

func TestArchitectureCheckerRegistersStableProviderInteractionFlow(t *testing.T) {
	interactionStore := testPackage("internal/interactionstore", "internal/domain", "internal/interactionsourcestore", "internal/runtimeprotocol")
	interactionStore.ProductionLines = 680
	interactionSourceStore := testPackage("internal/interactionsourcestore")
	interactionSourceStore.ProductionLines = 350
	packages := []packageInfo{
		testPackage("internal/interactionflow", "internal/coordinator", "internal/domain", "internal/interactionstore", "internal/runtimeprotocol", "internal/sessionruntime", "internal/telegrambridge", "internal/telegramcontroller", "internal/telegramflow", "internal/telegrampipeline", "internal/telegramui"),
		testPackage("internal/coordinator"),
		testPackage("internal/domain"),
		interactionStore,
		interactionSourceStore,
		testPackage("internal/runtimeprotocol"),
		testPackage("internal/sessionruntime", "internal/domain", "internal/runtimeprotocol"),
		testPackage("internal/telegrambridge"),
		testPackage("internal/telegramcontroller", "internal/domain", "internal/sessionruntime"),
		testPackage("internal/telegramflow", "internal/domain"),
		testPackage("internal/telegrampipeline", "internal/domain"),
		testPackage("internal/telegramui", "internal/domain"),
	}
	if errors := checkGraph(packages); len(errors) != 0 {
		t.Fatalf("checkGraph() errors = %v, want none", errors)
	}
}

func TestArchitectureCheckerRegistersManualCoordinatorTransfer(t *testing.T) {
	packages := []packageInfo{
		testPackage("internal/coordinatortransfer", "internal/computer", "internal/coordinatorbundle", "internal/domain", "internal/nodelink"),
		testPackage("internal/computer", "internal/domain"),
		testPackage("internal/coordinatorbundle"),
		testPackage("internal/domain"),
		testPackage("internal/nodelink", "internal/computer", "internal/domain"),
	}
	if errors := checkGraph(packages); len(errors) != 0 {
		t.Fatalf("checkGraph() errors = %v, want none", errors)
	}
	assertErrorContains(
		t,
		checkGraph(graphWithEdge("internal/coordinatortransfer", "internal/settings")),
		"package imports dependency outside registered boundary: internal/coordinatortransfer -> internal/settings",
	)
}

func TestArchitectureCheckerRegistersTransactionalCoordinatorStateStore(t *testing.T) {
	bundle := testPackage("internal/coordinatorbundle", "internal/computer", "internal/coordinator", "internal/domain", "internal/messagejournal", "internal/settings", "internal/telegramflow", "internal/telegrampipeline", "internal/telegramstate", "internal/telegramui")
	bundle.ProductionLines = 280
	packages := []packageInfo{
		testPackage("internal/coordinatorstate", "internal/coordinatorbundle", "internal/coordinatortransfer"),
		testPackage("internal/coordinatortransfer", "internal/computer", "internal/coordinatorbundle", "internal/domain", "internal/nodelink"),
		bundle,
		testPackage("internal/computer", "internal/domain"),
		testPackage("internal/coordinator"),
		testPackage("internal/domain"),
		testPackage("internal/messagejournal"),
		testPackage("internal/nodelink", "internal/computer", "internal/domain"),
		testPackage("internal/settings"),
		testPackage("internal/telegramflow"),
		testPackage("internal/telegrampipeline"),
		testPackage("internal/telegramstate"),
		testPackage("internal/telegramui"),
	}
	if errors := checkGraph(packages); len(errors) != 0 {
		t.Fatalf("checkGraph() errors = %v, want none", errors)
	}
	assertErrorContains(
		t,
		checkGraph(graphWithEdge("internal/coordinatortransfer", "internal/coordinatorstate")),
		"package imports dependency outside registered boundary: internal/coordinatortransfer -> internal/coordinatorstate",
	)
}

func TestArchitectureCheckerRejectsUnknownExternalOnlyEvidenceCategory(t *testing.T) {
	original := packagePolicies["internal/backup"]
	invalid := original
	invalid.externalOnlyEvidence = "not-a-release-category"
	packagePolicies["internal/backup"] = invalid
	t.Cleanup(func() { packagePolicies["internal/backup"] = original })
	assertErrorContains(t, checkGraph([]packageInfo{testPackage("internal/backup")}), "invalid external-only evidence category")
}

func TestArchitectureCheckerRegistersSecretFilePolicy(t *testing.T) {
	policy, ok := packagePolicies["internal/secretfile"]
	if !ok {
		t.Fatal("packagePolicies[internal/secretfile] is missing")
	}
	if policy.responsibility != "pass a bounded secret file to a callback with guaranteed transient zeroization" {
		t.Fatalf("responsibility = %q", policy.responsibility)
	}
	if len(policy.allowedImports) != 0 {
		t.Fatalf("allowedImports = %v, want stdlib-only", policy.allowedImports)
	}
	if policy.maxProductionLines != 200 {
		t.Fatalf("maxProductionLines = %d, want 200", policy.maxProductionLines)
	}
	if policy.externalOnlyEvidence != "" {
		t.Fatalf("externalOnlyEvidence = %q, want none", policy.externalOnlyEvidence)
	}

	assertErrorContains(t, checkGraph(graphWithEdge("internal/secretfile", "internal/safelog")), "package imports dependency outside registered boundary: internal/secretfile -> internal/safelog")
	if blockers := externalOnlyReleaseBlockers([]packageInfo{testPackage("internal/secretfile")}); len(blockers) != 0 {
		t.Fatalf("external-only blockers = %v, want none", blockers)
	}
}

func TestArchitectureCheckerRejectsAuthorizationFlowImportingProvider(t *testing.T) {
	errors := checkGraph(graphWithEdge("internal/authflow", "internal/provider/codex"))
	assertErrorContains(t, errors, "package imports dependency outside registered boundary: internal/authflow -> internal/provider/codex")
}

func TestArchitectureCheckerRejectsReverseNodeLinkExecutorDependency(t *testing.T) {
	errors := checkGraph(graphWithEdge("internal/nodelink", "internal/executor"))
	assertErrorContains(t, errors, "package imports dependency outside registered boundary: internal/nodelink -> internal/executor")
}

func TestArchitectureCheckerAllowsApprovedDependencies(t *testing.T) {
	packages := []packageInfo{
		testPackage("internal/config", "internal/domain"),
		testPackage("internal/workdir", "internal/app", "internal/domain"),
		testPackage("internal/telegram"),
		testPackage("internal/telegramapp", "internal/app", "internal/domain", "internal/telegramui"),
		testPackage("internal/telegramui", "internal/app", "internal/domain"),
		testPackage("internal/sessionid", "internal/app", "internal/domain"),
		testPackage("internal/provider/codex", "internal/domain"),
		testPackage("internal/provider/claude", "internal/domain"),
		testPackage("internal/app", "internal/domain"),
		testPackage("internal/domain"),
	}
	if errors := checkGraph(packages); len(errors) != 0 {
		t.Fatalf("checkGraph() errors = %v, want none", errors)
	}
}

func makeRepo(t *testing.T, policy string) string {
	t.Helper()
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "AGENTS.md"), policy)
	runGit(t, repo, "init", "--quiet")
	return repo
}

func readProjectPolicy(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() failed")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "..", "AGENTS.md"))
	if err != nil {
		t.Fatalf("read project AGENTS.md: %v", err)
	}
	return string(data)
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func runGit(t *testing.T, repo string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = repo
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
}

func assertErrorContains(t *testing.T, errors []string, substring string) {
	t.Helper()
	for _, message := range errors {
		if strings.Contains(message, substring) {
			return
		}
	}
	t.Fatalf("errors = %v, want substring %q", errors, substring)
}

func testPackage(path string, imports ...string) packageInfo {
	prefix := "bria/"
	qualified := make([]string, len(imports))
	for index, imported := range imports {
		qualified[index] = prefix + imported
	}
	return packageInfo{
		ImportPath:    prefix + path,
		RelativePath:  path,
		Imports:       qualified,
		HasProduction: true,
	}
}

func graphWithEdge(source, target string) []packageInfo {
	return []packageInfo{testPackage(source, target), testPackage(target)}
}

func graphWithTestEdge(source, target string, external bool) []packageInfo {
	sourcePackage := testPackage(source)
	if external {
		sourcePackage.XTestImports = []string{"bria/" + target}
	} else {
		sourcePackage.TestImports = []string{"bria/" + target}
	}
	return []packageInfo{sourcePackage, testPackage(target)}
}

func writeTarGZ(t *testing.T, archivePath, member string, contents []byte) {
	writeTarGZMembers(t, archivePath, map[string][]byte{member: contents})
}

func writeTarGZMembers(t *testing.T, archivePath string, members map[string][]byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(archivePath), 0o755); err != nil {
		t.Fatalf("mkdir release directory: %v", err)
	}
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	names := make([]string, 0, len(members))
	for name := range members {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, member := range names {
		contents := members[member]
		if err := tarWriter.WriteHeader(&tar.Header{Name: member, Mode: 0o700, Size: int64(len(contents)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatalf("write archive header: %v", err)
		}
		if _, err := tarWriter.Write(contents); err != nil {
			t.Fatalf("write archive contents: %v", err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}
}

func writeEvidenceManifest(t *testing.T, directory, version string, ids []string) string {
	t.Helper()
	releaseManifestContents := []byte("signed release identity for " + version + "\n")
	releaseManifestPath := filepath.Join(directory, "release-manifest.json")
	writeFile(t, releaseManifestPath, string(releaseManifestContents))
	artifactEntries := []string{fmt.Sprintf(
		`{"name":"release-manifest.json","sha256":"%x"}`,
		sha256.Sum256(releaseManifestContents),
	)}
	for _, target := range []string{"darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64"} {
		archiveName := "bria_" + version + "_" + target + ".tar.gz"
		bundle := strings.TrimSuffix(archiveName, ".tar.gz")
		members := make(map[string][]byte)
		for _, binary := range []string{"bria", "bria-codex-adapter", "bria-claude-adapter"} {
			contents := []byte(target + " executable " + binary + "\n")
			members[bundle+"/"+binary] = contents
			digest := sha256.Sum256(contents)
			artifactEntries = append(artifactEntries, fmt.Sprintf(
				`{"name":%q,"sha256":"%x"}`,
				archiveName+"!"+binary, digest,
			))
		}
		archivePath := filepath.Join(directory, archiveName)
		writeTarGZMembers(t, archivePath, members)
		archiveContents, err := os.ReadFile(archivePath)
		if err != nil {
			t.Fatalf("read release archive fixture: %v", err)
		}
		archiveDigest := sha256.Sum256(archiveContents)
		artifactEntries = append(artifactEntries, fmt.Sprintf(
			`{"name":%q,"sha256":"%x"}`,
			archiveName, archiveDigest,
		))
	}
	var entries []string
	observedAt := time.Now().UTC().Format(time.RFC3339)
	for _, id := range ids {
		contents := []byte("real external receipt for " + id + "\n")
		relative := filepath.ToSlash(filepath.Join("receipts", id+".txt"))
		writeFile(t, filepath.Join(directory, filepath.FromSlash(relative)), string(contents))
		digest := sha256.Sum256(contents)
		entries = append(entries, fmt.Sprintf(
			`{"id":%q,"status":"verified","environment":"test-host","scenario":"real end-to-end probe","observed_at":%q,"evidence_file":%q,"sha256":"%x"}`,
			id, observedAt, relative, digest,
		))
	}
	manifest := filepath.Join(directory, "release-evidence.json")
	writeFile(t, manifest, fmt.Sprintf(
		"{\"schema_version\":1,\"release_version\":%q,\"revision\":\"revision-1\",\"producer_run_id\":\"test-run-1\",\"generated_at\":%q,\"release_manifest_sha256\":\"%x\",\"artifacts\":[%s],\"receipts\":[%s]}\n",
		version, observedAt, sha256.Sum256(releaseManifestContents), strings.Join(artifactEntries, ","), strings.Join(entries, ","),
	))
	return manifest
}
