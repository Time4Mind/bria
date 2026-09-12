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

func TestNativeMigrationKeepsExactPackageBoundaries(t *testing.T) {
	for _, test := range []struct {
		path    string
		imports []string
	}{
		{"internal/nativeadapter", []string{"internal/domain", "internal/nativeacceptance", "internal/nativeattachment", "internal/nativecapture", "internal/nativecli", "internal/nativeeventkind", "internal/nativeinputowner", "internal/nativephotostaging", "internal/nativereceiptstore", "internal/nativestartupdiagnostic", "internal/nativeterminal", "internal/nativetranscript", "internal/runtimediagnostic", "internal/runtimeprotocol"}},
		{"internal/nativecapture", nil},
		{"internal/nativeattachment", nil},
		{"internal/nativecli", []string{"internal/domain", "internal/nativeapproval"}},
		{"internal/nativeapproval", nil},
		{"internal/nativecontrolport", []string{"internal/domain"}},
		{"internal/nativeapprovalflow", []string{"internal/domain", "internal/nativeapproval", "internal/nativecontrolport"}},
		{"internal/nativeterminal", []string{"internal/terminalbinding"}},
		{"internal/nativetranscript", []string{"internal/assistanttext", "internal/nativejsonline", "internal/runtimeprotocol", "internal/tooltext"}},
	} {
		policy, ok := packagePolicies[test.path]
		if !ok || policy.responsibility == "" || policy.compositionRoot || strings.Join(policy.allowedImports, "\x00") != strings.Join(test.imports, "\x00") {
			t.Fatalf("native policy %s = %#v", test.path, policy)
		}
		for _, target := range []string{"internal/storage", "internal/telegramcontroller", "internal/telegram", "internal/singlemachinecomposition"} {
			if errors := checkGraph(graphWithEdge(test.path, target)); len(errors) == 0 {
				t.Fatalf("native boundary improperly allows %s -> %s", test.path, target)
			}
		}
	}
	for _, command := range []string{"cmd/bria-codex-adapter", "cmd/bria-claude-adapter"} {
		for _, target := range []string{"internal/nativeadapter", "internal/domain"} {
			if errors := checkGraph(graphWithEdge(command, target)); len(errors) != 0 {
				t.Fatalf("native composition %s -> %s: %v", command, target, errors)
			}
		}
	}
}

func TestArchitectureCheckerRegistersInitialRecoverySupportBoundaries(t *testing.T) {
	for _, test := range []struct {
		path           string
		responsibility string
		imports        []string
		limit          int
	}{
		{
			path:           "internal/initialstartretry",
			responsibility: "retry initial provider starts and recover unbound starting sessions through exact durable transitions",
			imports:        []string{"internal/domain", "internal/providerattachport"},
			limit:          325,
		},
		{
			path:           "internal/nativestartupdiagnostic",
			responsibility: "classify payload-free native startup failures by bounded stage and class",
			imports:        []string{"internal/nativetranscript", "internal/runtimediagnostic"},
			limit:          100,
		},
		{
			path:           "internal/recoverybackoff",
			responsibility: "schedule per-session recovery attempts with lifecycle-aware capped exponential backoff",
			imports:        []string{"internal/domain"},
			limit:          100,
		},
	} {
		t.Run(test.path, func(t *testing.T) {
			policy, ok := packagePolicies[test.path]
			if !ok {
				t.Fatalf("packagePolicies[%q] is missing", test.path)
			}
			if policy.responsibility != test.responsibility ||
				strings.Join(policy.allowedImports, "\x00") != strings.Join(test.imports, "\x00") ||
				policy.maxProductionLines != test.limit || policy.compositionRoot || policy.externalOnlyEvidence != "" {
				t.Fatalf("recovery support policy %s = %#v", test.path, policy)
			}
		})
	}

	if errors := checkGraph(graphWithEdge("internal/app", "internal/initialstartretry")); len(errors) != 0 {
		t.Fatalf("app use-case split rejected: %v", errors)
	}
	assertErrorContains(
		t,
		checkGraph(graphWithEdge("internal/app", "internal/recoverybackoff")),
		"app imports infrastructure: internal/app -> internal/recoverybackoff",
	)
}

func TestArchitectureCheckerBudgetsLiveRecoveryComposition(t *testing.T) {
	policy := packagePolicies["internal/supervisioncomposition"]
	if policy.maxProductionLines != 500 || policy.responsibility != "compose startup and live supervision with exact provider and input-recovery boundaries" {
		t.Fatalf("supervision composition policy = %#v", policy)
	}
	recoveryBarrier := packagePolicies["internal/supervisioncomposition/recoverybarrier"]
	if recoveryBarrier.responsibility != "coordinate stable recovery evidence barriers with lifecycle-aware retry backoff" ||
		strings.Join(recoveryBarrier.allowedImports, "\x00") != strings.Join([]string{"internal/domain", "internal/recoverybackoff"}, "\x00") ||
		recoveryBarrier.maxProductionLines != 100 {
		t.Fatalf("recovery barrier policy = %#v", recoveryBarrier)
	}
}

func TestNativeAttachmentKeepsStdlibBoundaryAndThreeExactConsumers(t *testing.T) {
	policy := packagePolicies["internal/nativeattachment"]
	if policy.responsibility != "validate bounded native photo custody from content bytes" || len(policy.allowedImports) != 0 || policy.maxProductionLines != 100 {
		t.Fatalf("native attachment policy = %#v", policy)
	}
	wantConsumers := map[string]bool{
		"internal/nativeadapter":            true,
		"internal/nativephotostaging":       true,
		"internal/providerinputcomposition": true,
	}
	for source, registered := range packagePolicies {
		for _, target := range registered.allowedImports {
			if target == "internal/nativeattachment" {
				if !wantConsumers[source] {
					t.Fatalf("native attachment admitted unexpected consumer %s", source)
				}
				delete(wantConsumers, source)
			}
		}
	}
	if len(wantConsumers) != 0 {
		t.Fatalf("native attachment consumers missing: %v", wantConsumers)
	}
	assertErrorContains(
		t,
		checkGraph(graphWithEdge("internal/nativeattachment", "internal/domain")),
		"package imports dependency outside registered boundary: internal/nativeattachment -> internal/domain",
	)
}

func TestProviderModelsKeepsNarrowCatalogBoundary(t *testing.T) {
	policy := packagePolicies["internal/providermodels"]
	want := []string{"internal/config", "internal/domain", "internal/processenv", "internal/processgroup", "internal/settingsport"}
	if strings.Join(policy.allowedImports, "\x00") != strings.Join(want, "\x00") || policy.maxProductionLines != 275 || policy.responsibility != "collect bounded local CLI model catalogs with successful-result caching and process cleanup" {
		t.Fatalf("model catalog policy = %#v", policy)
	}
	for path, registered := range packagePolicies {
		for _, target := range registered.allowedImports {
			if target == "internal/providermodels" && path != "internal/singlemachinecomposition" {
				t.Fatalf("catalog must be composed only at single-machine root: %s", path)
			}
		}
	}
	for _, target := range []string{"internal/storage", "internal/telegramcontroller", "internal/telegram", "internal/providerquota"} {
		if errors := checkGraph(graphWithEdge("internal/providermodels", target)); len(errors) == 0 {
			t.Fatalf("catalog improperly allows %s", target)
		}
	}
}

var requiredInvariantLines = []string{
	"- единый неизменный договор задачи до начала работы;",
	"- весь запрос как единица завершения, сохранность todo, восстановление контекста и проверка каждого обязательства перед финалом;",
	"- карта покрытия до параллельного запуска;",
	"- максимальная безопасная параллельность и непересекающееся владение;",
	"- отдельные рабочие копии для конфликтующих изменений;",
	"- единственный владелец объединения;",
	"- проверка доказательств и явное описание неопределённости;",
	"- быстрые профильные проверки во время разработки и полный набор перед выпуском;",
	"- проверка физического результата;",
	"- точная bounded последовательность и точные цели с terminal criterion, явное подтверждение на действие или bounded sequence (включая узкое standing authorization для Bria), действующее до terminal criterion и включающее необходимые commit, push и CI-fix iterations, выполнение только согласованного scope, повторное чтение после каждого существенного внешнего write и новое подтверждение для новых, расширенных или destructive targets, deploy вне standing authorization, изменений secrets и исходящих messages.",
}

var requiredPolicyClauseLines = map[string][]string{
	"Task continuity": {
		"Перед многошаговой работой и после compaction обязательно восстановить текущий контекст по [docs/HARNESS.md](docs/HARNESS.md).",
		"Единица завершения — весь запрос пользователя со всеми действующими дополнениями, а не отдельная подзадача.",
		"Сохранять в одном durable todo все обязательства запроса с устойчивыми ID, критериями приёмки, статусами и доказательствами проверок; обновлять его при изменении объёма и перед передачей работы.",
		"Новые сообщения дополняют или уточняют текущий объём; вопрос о статусе не отменяет работу. Удалять обязательство или заменять задачу можно только по явному указанию пользователя.",
		"Восстанавливать текущий договор, все незакрытые пункты и ограничения без молчаливого усечения; читать связанные доказательства по необходимости, не всю историю. Backlog сохраняется, но сам по себе не разрешает новую работу.",
		"Сбой формата Harness не должен терять, обнулять или блокировать todo: сохранить исходные записи, продолжить безопасную разрешённую работу и локально исправить совместимость без требования к пользователю чинить метаданные. Нечитаемую запись нельзя считать выполненной.",
		"До объявления блокера проверить актуальные проектные указатели и доступные доказательства; старый блокер не доказывает отсутствие доступа сейчас. Выполнить всю независимую разрешённую подготовку, не расширяя полномочия.",
		"Перед финалом владелец объединения сверяет каждый пункт всего запроса с физическим результатом и проверкой актуальной версии. Если остаётся выполнимая разрешённая работа, продолжать её; частичный успех не завершает запрос.",
		"End-to-end включает затронутых потребителей и пользовательскую границу, включая live-сценарий, когда он необходим и разрешён. Локальная проверка не заменяет требуемую внешнюю приёмку; непроверенные границы остаются открытыми.",
		"При реальном блокере или необходимом новом разрешении дать краткий статус и точное требуемое решение, сохранив незакрытые пункты. Итоговый отчёт кратко перечисляет решённые проблемы и существенные ограничения; проверка метаданных не заменяет приёмку результата.",
	},
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
		"2. Получить одно явное подтверждение на отдельное действие или bounded sequence, если оно ещё не дано, в том числе standing authorization ниже. Подтверждение bounded sequence действует до указанного terminal criterion и включает необходимые commit, push и CI-fix iterations.",
		"3. Выполнить только этот согласованный scope.",
		"4. После каждого существенного внешнего write повторно прочитать целевое состояние и проверить фактический результат.",
		"Новые или расширенные targets требуют нового подтверждения.",
		"Deploy вне standing authorization ниже, изменения secrets, исходящие messages и любое destructive действие, включая destructive write к уже перечисленной цели, всегда требуют нового подтверждения, даже если они заранее перечислены.",
		"Неопределённый сетевой ответ не считать ни успехом, ни отказом: сначала безопасно перечитать состояние, затем решать вопрос о повторе.",
		"По явному указанию Артёма от 2026-09-08, повторно подтверждённому 2026-09-09, для обычной явно запрошенной разработки Bria действует standing authorization: после полного завершения разработки владелец объединения автоматически выполняет commit, push, merge при работе через ветку/PR и deploy с перезапуском без повторного запроса подтверждения. Review, диагностика и незавершённая задача не запускают выпуск; явные ограничения пользователя local-only, no-push или запрет restart имеют приоритет.",
		"Для этого выпуска подтверждение уже дано. Общие требования отдельного согласования внешних writes или deploy не являются причиной снова спрашивать разрешение либо останавливаться перед push, merge или restart. Это явное указание Артёма заменяет прежние конфликтующие правила и исторические записи ожидания согласования выпуска Bria. Оно не отменяет проверки готовности и ограничения targets ниже.",
		"Разрешённые targets: только текущий репозиторий Time4Mind/bria, обычные commit и push своей ветки задачи, её merge/PR merge в origin/main без force и обхода branch protection, и установка/перезапуск существующего локального сервиса gui/501/com.time4mind.bria.v2 на текущей машине. При работе непосредственно в main отдельный merge не нужен; нельзя создавать фиктивную ветку/PR ради этого шага. Другие клоны, репозитории, hosts и services не получают разрешения автоматически.",
		"До записи владелец объединения read-only проверяет фактические origin, ветку, service и пути установки, показывает точный manifest изменений и артефактов, bounded последовательность и terminal criterion. Git и deploy выполняет только владелец объединения последовательно; read-only исполнители не выполняют mutations.",
		"Последовательность выпуска: закрыть все обязательства разработки и проверить физический результат; выполнить полный make check-full; проверить и commit только согласованный manifest; выполнить обычный push. При работе через ветку/PR дождаться её обязательных checks и выполнить merge в main без обхода защиты; при работе в main выполнить push напрямую. Дождаться успешных обязательных CI для точного итогового SHA в origin/main; установить проверенные бинарники этого кода и перезапустить только указанный существующий Bria service. Необходимые CI-fix iterations в рамках задачи включены; изменённую версию снова полностью проверить до установки.",
		"После каждого существенного write повторно read-only проверить целевое состояние. Terminal criterion: remote HEAD соответствует проверенному commit, обязательные CI этого SHA зелёные, установленные версии и hashes совпадают с проверенными артефактами, process/lock и свежие безопасные логи подтверждают healthy service, пользовательские settings/session/history/journal сохранены. Не выдавать непроверенный live-сценарий за доказанный.",
		"Standing authorization не разрешает изменения secrets, config/state, исходящие messages, destructive операции или расширение targets; для них нужно отдельное явное разрешение. Ограничения и запросы разрешений инструментов более высокого приоритета сохраняются; этот текст их не обходит.",
	},
}

func TestPolicyCheckerProtectsAutomaticReleaseAmendment(t *testing.T) {
	original := readProjectPolicy(t)
	for _, test := range []struct{ name, clause string }{
		{"automatic release", "автоматически выполняет commit, push, merge при работе через ветку/PR и deploy с перезапуском без повторного запроса подтверждения."},
		{"no repeat approval", "Общие требования отдельного согласования внешних writes или deploy не являются причиной снова спрашивать разрешение либо останавливаться перед push, merge или restart."},
		{"supersedes old approval pauses", "Это явное указание Артёма заменяет прежние конфликтующие правила и исторические записи ожидания согласования выпуска Bria."},
		{"protected merge", "её merge/PR merge в origin/main без force и обхода branch protection"},
		{"no fake merge", "При работе непосредственно в main отдельный merge не нужен; нельзя создавать фиктивную ветку/PR ради этого шага."},
		{"branch checks and direct main", "При работе через ветку/PR дождаться её обязательных checks и выполнить merge в main без обхода защиты; при работе в main выполнить push напрямую."},
		{"exact final main SHA", "Дождаться успешных обязательных CI для точного итогового SHA в origin/main; установить проверенные бинарники этого кода и перезапустить только указанный существующий Bria service."},
	} {
		t.Run(test.name, func(t *testing.T) {
			if !strings.Contains(original, test.clause) {
				t.Fatalf("amended AGENTS.md lacks the acceptance fixture %q", test.clause)
			}
			repo := makeRepo(t, strings.Replace(original, test.clause, "", 1))
			// Require the removed rule's diagnostic, not an unrelated failure
			// from an outdated policy clause or another repository check.
			assertErrorContains(t, checkPolicy(repo), test.clause)
		})
	}
}

func TestPolicyCheckerRejectsRestoredUnconditionalDeployApproval(t *testing.T) {
	original := readProjectPolicy(t)
	obsolete := "Deploy, изменения secrets, исходящие messages и любое destructive действие, включая destructive write к уже перечисленной цели, всегда требуют нового подтверждения, даже если они заранее перечислены."
	for _, test := range []struct{ name, policy string }{
		{"alongside standing authorization", strings.Replace(original, "### Standing authorization for Bria release", obsolete+"\n\n### Standing authorization for Bria release", 1)},
		{"appended requirement", original + "\n" + obsolete + "\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := makeRepo(t, test.policy)
			assertErrorContains(t, checkPolicy(repo), "conflicting unconditional deploy approval")
		})
	}
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

func TestMakefileSeparatesRaceCgoFromOfflineNonRaceEnvironment(t *testing.T) {
	makefile := readMakefile(t)
	if !strings.Contains(makefile, "CGO_ENABLED=0") {
		t.Fatal("Makefile must keep CGO disabled for the non-race environment")
	}
	if !strings.Contains(makefile, "GO_RACE_ENV = $(GO_ENV) CGO_ENABLED=1") {
		t.Fatal("Makefile must define the race environment as GO_ENV plus CGO_ENABLED=1")
	}
	raceRecipe := makefileTargetRecipe(t, makefile, "check-race")
	if !strings.Contains(raceRecipe, "$(GO_RACE_ENV) $(GO) test -race") {
		t.Fatalf("check-race recipe = %q, must use GO_RACE_ENV", raceRecipe)
	}
	if strings.Contains(raceRecipe, "$(GO_ENV) $(GO) test -race") {
		t.Fatalf("check-race recipe = %q, must not use the CGO-disabled environment", raceRecipe)
	}
	for _, target := range []string{"check-test", "check-vet"} {
		recipe := makefileTargetRecipe(t, makefile, target)
		if !strings.Contains(recipe, "$(GO_ENV) $(GO)") {
			t.Fatalf("%s recipe = %q, must keep the non-race environment", target, recipe)
		}
	}
}

func TestPolicyCheckerAllowsUntrackedPolicyBeforeFirstCommit(t *testing.T) {
	repo := makeRepo(t, readProjectPolicy(t))
	if errors := checkPolicy(repo); len(errors) != 0 {
		t.Fatalf("checkPolicy() errors = %v, want none", errors)
	}
}

func TestPolicyCheckerRejectsMissingPolicy(t *testing.T) {
	repo := makeRepo(t, readProjectPolicy(t))
	if err := os.Remove(filepath.Join(repo, "AGENTS.md")); err != nil {
		t.Fatalf("remove AGENTS.md: %v", err)
	}
	assertErrorContains(t, checkPolicy(repo), "AGENTS.md is missing")
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

func TestPolicyCheckerRejectsIgnoredPolicyAfterFirstCommit(t *testing.T) {
	repo := makeRepo(t, readProjectPolicy(t))
	writeFile(t, filepath.Join(repo, ".gitignore"), "AGENTS.md\n")
	writeFile(t, filepath.Join(repo, "README.md"), "# Test repository\n")
	runGit(t, repo, "add", ".gitignore", "README.md")
	runGit(
		t,
		repo,
		"-c", "user.name=Bria Tests",
		"-c", "user.email=bria-tests@example.invalid",
		"commit", "--quiet", "-m", "initial",
	)
	assertErrorContains(t, checkPolicy(repo), "excluded by a Git ignore rule")
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

func TestReleaseArtifactBearerScanRequiresAuthorizationContext(t *testing.T) {
	repo := makeRepo(t, readProjectPolicy(t))
	artifact := filepath.Join(repo, "bin", "bria")
	writeFile(t, artifact, "linked binary text\x00Bearer "+strings.Repeat("adjacent", 4))
	if errors := checkSecretContents(repo); len(errors) != 0 {
		t.Fatalf("checkSecretContents() errors = %v, want no generic binary Bearer false positive", errors)
	}

	writeFile(t, artifact, "Authorization: Bearer "+strings.Repeat("sensitive", 4))
	assertErrorContains(t, checkSecretContents(repo), "literal bearer credential")
}

func TestSecretContentCheckerAllowsPlaceholdersAndOrdinarySource(t *testing.T) {
	repo := makeRepo(t, readProjectPolicy(t))
	writeFile(t, filepath.Join(repo, "config.example.json"), "{\"telegram_token\":\"${TELEGRAM_TOKEN}\"}\n")
	if errors := checkSecretContents(repo); len(errors) != 0 {
		t.Fatalf("checkSecretContents() errors = %v, want none", errors)
	}
}

func TestWorkflowCheckoutCredentialsAreNeverPersisted(t *testing.T) {
	repo := makeRepo(t, readProjectPolicy(t))
	workflow := filepath.Join(repo, ".github/workflows/check.yml")
	writeFile(t, workflow, "jobs:\n  check:\n    steps:\n      - uses: actions/checkout@pinned\n")
	runGit(t, repo, "add", ".github/workflows/check.yml")
	assertErrorContains(t, checkFilenames(repo), "workflow checkout persists Git credentials")

	writeFile(t, workflow, "jobs:\n  check:\n    steps:\n      - uses: actions/checkout@pinned\n        with:\n          persist-credentials: false\n")
	if errors := checkFilenames(repo); len(errors) != 0 {
		t.Fatalf("checkFilenames() errors = %v, want none", errors)
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
			responsibility: "compose durable message custody, accepted-turn reconciliation and recovery finalization",
			imports:        []string{"internal/turncontinuation", "internal/acceptedrecovery", "internal/domain", "internal/durableflow", "internal/durableinputbridge", "internal/durableoutputwait", "internal/messagejournal", "internal/sessionruntime", "internal/sessionsupervisor", "internal/telegramcontroller", "internal/telegramnotify", "internal/turnprocessing"},
			limit:          575,
		},
		{
			path:           "internal/acceptedinput",
			responsibility: "verify exact input custody and commit observed provider acceptance",
			imports:        []string{"internal/messagejournal"},
			limit:          200,
		},
		{
			path:           "internal/acceptedcontinuation",
			responsibility: "select whether retained accepted work yields to a newer durable input",
			imports:        []string{"internal/domain", "internal/messagejournal", "internal/sessionsupervisor"},
			limit:          150,
		},
		{
			path:           "internal/acceptedrecovery",
			responsibility: "reconcile exact accepted history and fence archived session resume",
			imports:        []string{"internal/acceptedrecovery/evidence", "internal/domain", "internal/durableflow", "internal/sessionruntime", "internal/sessionsupervisor", "internal/turncontinuation"},
			limit:          300,
		},
		{
			path:           "internal/acceptedrecovery/evidence",
			responsibility: "fingerprint payload-free journal and provider metadata for stable recovery barriers",
			imports:        []string{"internal/domain", "internal/durableflow", "internal/sessionruntime", "internal/sessionsupervisor"},
			limit:          150,
		},
		{
			path:           "internal/telegramruntimecomposition",
			responsibility: "project typed Telegram controller actions, coordinate current-surface status refreshes, signed model selectors and durable delivery receipts",
			imports:        []string{"internal/controllertelemetry", "internal/telegrambridge", "internal/telegramtrace", "internal/coordinator", "internal/domain", "internal/telegramcontroller", "internal/telegramflow", "internal/telegrampipeline", "internal/telegramrecoverycomposition", "internal/telegramstate", "internal/telegramui"},
			limit:          900,
		},
		{
			path:           "internal/telegrampromptcomposition",
			responsibility: "refresh active prompt and native-screen cards with visibility-scoped cancellation",
			imports:        []string{"internal/carddeliveryguard", "internal/coordinator", "internal/domain", "internal/inputcarrierguard", "internal/telegrambridge", "internal/telegramcontroller", "internal/telegramflow", "internal/telegramnotify", "internal/telegramstate", "internal/telegramui"},
			limit:          225,
		},
		{
			path:           "internal/turnruntimecomposition",
			responsibility: "assemble the controller-facing P4 turn path and durable per-session model preferences after state and safelog are open",
			imports:        []string{"internal/artifactruntimecomposition", "internal/config", "internal/domain", "internal/observability", "internal/observabilitycomposition", "internal/p4runtimecomposition", "internal/providerinputcomposition", "internal/safelog", "internal/sessionruntime", "internal/settings", "internal/storage", "internal/telegram", "internal/telegrambridge", "internal/telegramflow", "internal/turnprocessing"},
			limit:          250,
		},
		{
			path:           "internal/preprocessinglog",
			responsibility: "record payload-free prompt preprocessing lifecycle with shared input correlation",
			imports:        []string{"internal/observability", "internal/promptpreprocess", "internal/promptpreprocesssession", "internal/safelog"},
			limit:          100,
		},
		{
			path:           "internal/telegramcardretirement",
			responsibility: "retire the exact previous session-card keyboard before a replacement carrier is sent",
			imports:        []string{"internal/domain", "internal/telegramstate"},
			limit:          100,
		},
		{
			path:           "internal/telegramcompletionpolicy",
			responsibility: "evaluate active and background completion-notification visibility",
			imports:        []string{"internal/carddeliveryguard", "internal/domain", "internal/settingsport", "internal/telegramstate"},
			limit:          50,
		},
		{
			path:           "internal/telegramcallbackack",
			responsibility: "persist one-shot callback acknowledgements independently from card delivery",
			imports:        []string{"internal/telegrambridge", "internal/telegramops"},
			limit:          200,
		},
		{
			path:           "internal/singlemachinecomposition",
			responsibility: "compose the single-computer Bria process and bind post-commit status refresh delivery",
			imports: []string{
				"internal/providermodels",
				"internal/acceptedcontinuation", "internal/app", "internal/authcomposition", "internal/callbacktoken", "internal/claudestore", "internal/config", "internal/coordinator", "internal/domain", "internal/durablecomposition", "internal/durableflow", "internal/interactioncomposition", "internal/messagejournal", "internal/nativerecoverycomposition", "internal/observability", "internal/preprocessinglog", "internal/processenv", "internal/promptpreprocesscommand", "internal/promptpreprocesssession", "internal/providerquota", "internal/recoverycomposition", "internal/recoveryruntime", "internal/runtimefactory", "internal/safelog", "internal/screenproduction", "internal/sessioncreation", "internal/sessiondeliverygate", "internal/sessionexpiry", "internal/sessionid", "internal/sessionnaming", "internal/sessionruntime", "internal/sessionsupervisor", "internal/settings", "internal/settingscomposition", "internal/storage", "internal/supervisioncomposition", "internal/telegram", "internal/telegrambridge", "internal/telegramcompletioncomposition", "internal/telegramcontroller", "internal/telegramflow", "internal/telegramnotify", "internal/telegrampipeline", "internal/telegrampromptcomposition", "internal/telegramrecoverycomposition", "internal/telegramruntimecomposition", "internal/turnruntimecomposition", "internal/workdir",
			},
			limit: 1250,
		},
		{
			path:           "internal/p4runtimecomposition",
			responsibility: "compose opt-in P4 media and Screen runtime adapters",
			imports:        []string{"internal/config", "internal/documentproduction", "internal/domain", "internal/inputcomposition", "internal/mediaproduction", "internal/providerinputcomposition", "internal/screen", "internal/screenproduction", "internal/sessionruntime", "internal/settings", "internal/speech/parakeet", "internal/storage", "internal/telegram", "internal/turnprocessing"},
			limit:          200,
		},
		{
			path:           "internal/observability",
			responsibility: "record safe terminal timing and non-blocking Telegram flow measurements",
			imports:        []string{"internal/controllertelemetry", "internal/safelog", "internal/telegramtrace"},
			limit:          350,
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
		"internal/app", "internal/config", "internal/coordinator", "internal/domain", "internal/instancelock", "internal/sessionruntime", "internal/settings", "internal/singlemachinecomposition", "internal/statecompatibility", "internal/storage", "internal/telegram", "internal/telegrambridge", "internal/telegramnotify",
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
		{path: "internal/messagejournal", limit: 1800},
		{path: "internal/sessionsupervisor", limit: 500},
		{path: "internal/telegramflow", limit: 3150},
		{path: "internal/mutationscheduler", limit: 700},
		{path: "internal/telegramcallbackack", limit: 200},
		{path: "internal/telegramcardretirement", limit: 100},
		{path: "internal/telegramcompletionpolicy", limit: 50},
		{path: "internal/telegrampipeline", limit: 1800},
		{path: "internal/sessionruntime", limit: 1850},
		{path: "internal/orphanresume", limit: 150},
		{path: "internal/coordinator", limit: 850},
		{path: "internal/nativeadapter", limit: 875},
		{path: "internal/nativeattachment", limit: 100},
		{path: "internal/nativecli", limit: 600},
		{path: "internal/nativeapproval", limit: 450},
		{path: "internal/nativecontrolport", limit: 60},
		{path: "internal/nativeapprovalflow", limit: 175},
		{path: "internal/nativecapture", limit: 60},
		{path: "internal/documentproduction", limit: 100},
		{path: "internal/providerpreferences", limit: 100},
		{path: "internal/settingscodec", limit: 150},
		{path: "internal/telegramcallbackview", limit: 550},
		{path: "internal/telegramhistory", limit: 100},
		{path: "internal/telegramrich", limit: 250},
		{path: "internal/nativerender", limit: 550},
		{path: "internal/pngprofile", limit: 250},
		{path: "internal/nativescreencache", limit: 400},
		{path: "internal/assistanttext", limit: 125},
		{path: "internal/telegramturnhelpers", limit: 425},
		{path: "internal/mediaproduction", limit: 800},
		{path: "internal/screen", limit: 750},
		{path: "internal/screenproduction", limit: 200},
		{path: "internal/settings", limit: 825},
		{path: "internal/settingscomposition", limit: 250},
		{path: "internal/runtimeprotocol", limit: 1150},
		{path: "internal/telegrampromptcomposition", limit: 225},
		{path: "internal/telegramsettingsview", limit: 350},
		{path: "internal/nativeterminal", limit: 510},
		{path: "internal/nativetranscript", limit: 1100},
		{path: "internal/providerquota", limit: 350},
		{path: "internal/promptpreprocessbinding", limit: 450},
		{path: "internal/promptpreprocesscore", limit: 1050},
		{path: "internal/promptpreprocesssession", limit: 750},
		{path: "internal/storage", limit: 2100},
		{path: "internal/statejson", limit: 100},
		{path: "internal/telegram", limit: 1800},
		{path: "internal/telegramcompletioncomposition", limit: 275},
		{path: "internal/telegramcontroller", limit: 5900},
		{path: "internal/finalpersist", limit: 200},
		{path: "internal/turnadmission", limit: 120},
		{path: "internal/telegramsemantic", limit: 200},
		{path: "internal/domain", limit: 800},
		{path: "internal/telegrambridge", limit: 1550},
		{path: "internal/telegramui", limit: 800},
		{path: "internal/providermodels", limit: 275},
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

func TestArchitectureCheckerAllowsTelegramMutationSchedulerDependency(t *testing.T) {
	if errors := checkGraph(graphWithEdge("internal/telegram", "internal/mutationscheduler")); len(errors) != 0 {
		t.Fatalf("Telegram scheduler boundary errors = %v, want none", errors)
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

func TestArchitectureCheckerAllowsBridgeExternalScreenFixtureWithoutDomainRelaxation(t *testing.T) {
	if errors := checkGraph(graphWithTestEdge("internal/telegrambridge", "internal/screen", true)); len(errors) != 0 {
		t.Fatalf("external screen fixture rejected: %v", errors)
	}
	assertErrorContains(
		t,
		checkGraph(graphWithEdge("internal/telegrambridge", "internal/screen")),
		"package imports dependency outside registered boundary: internal/telegrambridge -> internal/screen",
	)
	assertErrorContains(
		t,
		checkGraph(graphWithTestEdge("internal/telegrambridge", "internal/domain", true)),
		"Telegram bridge imports package outside transport adaptation: internal/telegrambridge -> internal/domain",
	)
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
		testPackage("internal/speech/parakeet", "internal/processgroup", "internal/speech"),
		testPackage("internal/processgroup"),
		testPackage("internal/speech"),
	}
	if errors := checkGraph(packages); len(errors) != 0 {
		t.Fatalf("checkGraph() errors = %v, want none", errors)
	}
}

func TestArchitectureCheckerRegistersParakeetInstallerWithoutRuntimeCoupling(t *testing.T) {
	installer := testPackage("internal/parakeetinstall")
	installer.ProductionLines = 650
	if errors := checkGraph([]packageInfo{installer}); len(errors) != 0 {
		t.Fatalf("checkGraph() errors = %v, want none", errors)
	}
	assertErrorContains(
		t,
		checkGraph(graphWithEdge("internal/parakeetinstall", "internal/config")),
		"package imports dependency outside registered boundary: internal/parakeetinstall -> internal/config",
	)
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

func TestArchitectureCheckerRegistersAssistantTextIsolation(t *testing.T) {
	policy, ok := packagePolicies["internal/assistanttext"]
	if !ok {
		t.Fatal("assistanttext package policy is missing")
	}
	if policy.responsibility != "remove strictly validated service envelopes from assistant finals without I/O" || len(policy.allowedImports) != 0 || policy.maxProductionLines != 125 {
		t.Fatalf("assistanttext policy = %#v", policy)
	}
	packages := []packageInfo{
		testPackage("internal/assistanttext"),
		testPackage("internal/nativetranscript", "internal/assistanttext", "internal/nativejsonline", "internal/runtimeprotocol", "internal/tooltext"),
		testPackage("internal/cardtranscript", "internal/assistanttext", "internal/tooltext"),
		testPackage("internal/nativejsonline"),
		testPackage("internal/runtimeprotocol"),
		testPackage("internal/tooltext"),
	}
	if problems := checkGraph(packages); len(problems) != 0 {
		t.Fatalf("assistant text isolation graph errors = %v", problems)
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
			responsibility: "project typed provider events and active native session snapshots into virtual screen and optional Telegram media",
			imports:        []string{"internal/domain", "internal/nativescreencache", "internal/screen", "internal/sessionruntime", "internal/settings", "internal/telegram", "internal/turnprocessing"},
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
			imports:        []string{"internal/claudestore", "internal/domain", "internal/nativereceiptstore", "internal/nativetranscript", "internal/processgroup", "internal/runtimeprotocol", "internal/sessionruntime"},
			limit:          575,
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
		{path: "internal/screenproduction", imports: []string{"internal/domain", "internal/nativescreencache", "internal/screen", "internal/sessionruntime", "internal/settings", "internal/telegram", "internal/turnprocessing"}},
		{path: "internal/containerpreflight", imports: []string{"internal/config"}, evidence: "platform_docker_executor"},
		{path: "internal/recoveryruntime", imports: []string{"internal/claudestore", "internal/domain", "internal/nativereceiptstore", "internal/nativetranscript", "internal/processgroup", "internal/runtimeprotocol", "internal/sessionruntime"}},
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
			imports: []string{"internal/cardactivity", "internal/cardeventhistory", "internal/statejson", "internal/archiveimport", "internal/cardhistory", "internal/cardtranscript", "internal/coordinator", "internal/domain", "internal/sessionlabel", "internal/telegramhistory", "internal/telegramstate"},
		},
		{
			path:    "internal/recoveryruntime",
			imports: []string{"internal/claudestore", "internal/domain", "internal/nativereceiptstore", "internal/nativetranscript", "internal/processgroup", "internal/runtimeprotocol", "internal/sessionruntime"},
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
			imports:        []string{"internal/domain", "internal/nativeattachment", "internal/sessionruntime", "internal/turnprocessing"},
			limit:          250,
			evidence:       "",
		},
		{
			path:           "internal/settingsport",
			responsibility: "define the storage-neutral preferences boundary used by Telegram control surfaces",
			imports:        []string{"internal/domain", "internal/settingscapability"},
			limit:          125,
		},
		{
			path:           "internal/settingscomposition",
			responsibility: "compose neutral Telegram settings ports with canonical local settings and configuration stores",
			imports:        []string{"internal/domain", "internal/providerpreferences", "internal/settings", "internal/settingsport"},
			limit:          250,
		},
		{
			path:           "internal/telegramsettings",
			responsibility: "apply Telegram settings through neutral preferences ports",
			imports:        []string{"internal/domain", "internal/sessioncreation", "internal/settingsport"},
			limit:          200,
		},
		{
			path:           "internal/telegramsettingsview",
			responsibility: "render escaped grouped settings tables through neutral preferences ports",
			imports:        []string{"internal/domain", "internal/settingsport"},
			limit:          350,
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
	// Keep repository-check fixtures isolated from the caller's real release
	// directory. Make exports an absolute DIST_DIR for production checks; without
	// this override every fixture rescans all real release archives under -race.
	t.Setenv("DIST_DIR", filepath.Join(repo, "dist"))
	writeFile(t, filepath.Join(repo, "AGENTS.md"), policy)
	writeFile(t, filepath.Join(repo, "docs", "HARNESS.md"), "# Harness fixture\n")
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

func readMakefile(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() failed")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "..", "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	return string(data)
}

func makefileTargetRecipe(t *testing.T, makefile, target string) string {
	t.Helper()
	lines := strings.Split(makefile, "\n")
	start := -1
	for index, line := range lines {
		if line == target+":" {
			start = index + 1
			break
		}
	}
	if start == -1 {
		t.Fatalf("Makefile target %q is missing", target)
	}
	var recipe []string
	for _, line := range lines[start:] {
		if !strings.HasPrefix(line, "\t") {
			break
		}
		recipe = append(recipe, line)
	}
	return strings.Join(recipe, "\n")
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
