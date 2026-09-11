# A46: RCA повторяющихся session/provider failures

## Договор задачи

- Результат: классифицировать повторяющиеся session, provider, recovery и
  transport failures, доказать причину и влияние каждого существенного класса.
- Источники: безопасные структурные логи Bria, journal/state metadata и текущий
  код; пользовательские payload и значения secrets не читаются и не выводятся.
- Период: с запуска `20260911-git-credential-cleanup` до текущего времени,
  Europe/Moscow; при необходимости причинная цепочка расширяется до первого
  возникновения того же fingerprint.
- Единица: одно отдельное возникновение с session/correlation ID. Повторные
  строки одного события не считаются новыми ошибками.
- Writes: только этот todo и указатель статуса. Runtime, config, state, secrets,
  Telegram и Git release на этапе диагностики не меняются.
- Приёмка: для каждого частого класса указаны count/window, затронутая сессия,
  пользовательское влияние, доказанный code seam, минимальная репродукция и
  целевой regression probe; гипотезы отделены от фактов.

## Coverage map

| ID | Зона | Результат | Статус |
|---|---|---|---|
| A46.1 | Safe log inventory | Fingerprints, distinct counts и период | verified |
| A46.2 | Runtime/state correlation | Точные пострадавшие sessions/inputs без payload | verified |
| A46.3 | Code trace | Условие возникновения каждого класса | verified |
| A46.4 | Reproduction design | Минимальный RED probe на публичной границе | verified |

## Начальные проверяемые гипотезы

1. Один provider failure размножается periodic recovery sweep, хотя underlying
   process уже terminal.
2. Несколько независимых provider operations падают одинаково и ошибочно
   выглядят одним повтором.
3. Session lifecycle принимает нормальное provider completion за failure из-за
   гонки generation/process ownership.
4. Transport failures Telegram и provider failures независимы и не должны
   объединяться в один RCA.

Integration owner выполняет synthesis. Параллельные read-only зоны не меняют
файлы; при последующей реализации один пакет получает одного write-owner.

## Проверенный итог

Текущий release `20260911-git-credential-cleanup` работает с PID 6768. В его
окне с 12:09:36 MSK до последней доступной записи 13:06:58 MSK нет ни одного
`failed`, error category, `recovery_unknown` или `start_failed`. В durable state
10 основных сессий: 7 archived и 3 ready; `awaiting_recovery`, pending и
accepted input отсутствуют.

### Уже устранённые аварийные циклы

| ID | События и период, MSK | Причина и влияние | Статус |
|---|---|---|---|
| A46.F1 | 680 `session.recovery_failed`, 09.09 12:56-13:52, обычно 2 каждые 10 s | Initial session без binding застревала в `awaiting_recovery`; recovery не имел корректного terminal/backoff пути. Создание выглядело потерянным, лог штормил | исправлено `1240d77`: bounded initial retry, terminal cleanup и backoff |
| A46.F2 | 302 `controller.stopped` и 604 `session.recovery_failed`, 10.09 18:26-19:25, restart примерно каждые 10 s | Обычный message update с legacy keyboard ошибочно попадал в callback recovery; invalid recovery identity завершала весь controller. Luna стартовала заново как следствие, а не как причина | исправлено `eeb4bb5`; после hotfix crash-loop не повторялся |
| A46.F3 | 42 `session.provider_failure`, 11.09 09:17-09:31, одна session/input identity | Первый recovery cutoff не включал старый input в фазе `accepted`; dispatcher повторно отдавал один уже потерянный запрос после каждого unknown outcome | исправлено `583e895`: captured recovery prefix включает `accepted`; текущая очередь чистая |

### Дефекты, остающиеся в текущем коде

| ID | Доказательство | Пользовательское влияние | Целевой regression probe |
|---|---|---|---|
| A46.O1 | После архивации старый `select_session` callback 11.09 10:57 MSK успел выполнить Telegram edit, затем `commitCard` упал с `card_commit_failed`; operation осталась `receipt_confirmed`, status - `send_unknown`. `SemanticSelect` безусловно ставит `makeActive=true` и строит карточку даже когда `use` уже вернул статус недоступной archived session | Карточка визуально возвращается к уже архивированной сессии; durable UI state расходится с Telegram до следующего ручного действия | Выдать callback, архивировать target, нажать старую кнопку: archived card не редактируется, fallback остаётся active, операция завершается без `send_unknown` |
| A46.O2 | Ручной Stop 11.09 11:08 MSK дал native `turn_aborted/interrupted`, но controller записал `session.provider_failure`; штатные остановки при restart 09:17 и 11:54 также попали в provider failure. Условие сейчас - любой `err != nil`, без учёта terminal status | Ложная авария модели в логах; реальные provider failures нельзя отличить от Stop/restart | Public turn lifecycle: user Stop и service cancellation получают `cancelled/interrupted`, а настоящий provider error остаётся `provider_failure` |
| A46.O3 | 13 Luna `start_failed` имеют только общий класс `provider`; безопасный `thread_not_found` из adapter protocol теряется. После persistent satellite пары относятся к exact-resume пустого warm thread и завершаются replacement/`ready`; два последних current startup прошли сразу | Самовосстановление работает, но по service log нельзя отличить ожидаемый replacement от network/auth/provider сбоя | Startup log сохраняет allowlisted `error_code` и replacement outcome без thread ID/payload; empty-thread, transient и auth tests дают разные классы |
| A46.O4 | Три подряд identity-only `getMe` вернули одинаковый `transient Telegram failure`, затем тот же probe прошёл за 0.31 s без изменения credential. Production HTTP wrapper удаляет исходный transport class | Сбой был реальным и повторным, но существующий лог не позволяет доказать DNS/connect/TLS/reset/timeout причину или корректно алертить | Безопасная allowlist transport classes + retry test; сообщение не содержит URL, token и raw provider error |

## Остаточные записи, не являющиеся текущей аварией

- 183 старых startup error `missing settings revision` относятся к
  несовместимому сохранённому state и прекращаются после локальной миграции.
- Три старых outbound status `send_unknown` остаются forensic receipts и сейчас
  не replay-ятся. Один из них - доказанный A46.O1; два относятся к старым close
  operations с неопределённым Telegram receipt.
- Из 45 записей `session.provider_failure` 42 относятся к A46.F3, одна - к
  ручному Stop и две - к restart cancellation. Доказанного настоящего падения
  модели в этой выборке нет.

## Граница

Диагностика не меняла runtime, state, config, secrets или Telegram. Причина
трёх transient `getMe` ниже безопасного transport class недоказуема по текущей
телеметрии; A46.O4 нужен именно для следующего воспроизводимого случая.

## Реализация A47 - 2026-09-11

Артём явно разрешил исправить все четыре открытых дефекта A46.O1-A46.O4.
Договор расширен только на product code, behavioral tests и docs текущего
репозитория, после чего действует standing release sequence в `origin/main` и
существующий сервис `gui/501/com.time4mind.bria.v2`. Runtime state, config,
secrets и исходящие Telegram messages вручную не меняются.

| ID | Зона и owner | Критерий | Статус |
|---|---|---|---|
| A47.1 | `internal/telegramcontroller`, `internal/observabilitycomposition`; integration owner | Stale archived select не меняет active/card; Stop и shutdown не дают provider failure | focused green |
| A47.2 | `internal/promptpreprocesssession`; worker owner | Safe startup reason и replacement outcome доходят в service log | focused green |
| A47.3 | `internal/telegram`; worker owner | DNS/connect/TLS/reset/timeout получают разные безопасные classes без URL/token/raw error | focused green |
| A47.4 | read-only cross-check; reviewer | Public seams и security boundaries независимо проверены | verified: финальный review approve после двух итераций fixes |
| A47.5 | `internal/singlemachinecomposition`; integration owner | Production controller разрешает document input при подключённом safe preparer | focused green |
| A47.6 | archive projection; integration owner | После confirmed archive закрытая session сразу отсутствует в session buttons, а foreground показывает previous-active fallback | focused green |
| A47.7 | creation projection; integration owner | Сразу после create click persisted `starting` session видна кнопкой; provider startup продолжает FIFO и не блокирует UI | focused green |
| A47.8 | preprocessing publication; integration owner | Успешно очищенный prompt физически обновляет активную карточку до запуска основной модели; screenshot берётся в том же edit | focused green |
| A47.9 | output reconciliation; worker owner | После restart старая output lease истекает и pending card автоматически публикуется без пользовательского wake | focused green |
| A47.10 | archive/select gate; integration owner | Archive, stale select и физический Telegram edit сериализованы; archived session не может вернуться active или отредактировать карточку | focused green |
| A47.11 | default Starting fallback; integration owner | Starting default session сразу видна, принимает FIFO input и выбирается fallback при archive текущей session | focused green |
| A47.12 | document policy; worker owner | Отсутствующий Telegram `file_size` допускает bounded download; фактический oversize отклоняется | focused green |
| A47.13 | Luna/cancellation edges; worker + integration owner | Codex `thread_not_found` при доступном Claude всё равно запускает fresh Luna replacement; только доказанная остановка подавляет cancellation failure | focused green |
| A47.14 | preprocessing settings; worker owner | `Изменить инструкцию` показывает текущую инструкцию копируемым текстом; `CLI` action не падает | verified locally: current instruction paged below 4096 bytes; one-shot/degraded CLI inventory tests green |
| A47.15 | creation terminology; worker owner | Текст и кнопки создания сессии однозначно различают CLI/provider и backend без ошибочного маршрута | verified locally: CLI wording and disappearing-inventory regression green |
| A47.16 | background final navigation; worker owner | Открытие новой карточки финала фоновой session делает старую presentation неинтерактивной | verified locally: real ControllerFlowAdapter/store transition, stale retry and crash convergence race tests green |
| A47.17 | transcript isolation; worker + integration owner | Служебный memory/instruction payload не попадает в карточку workdir9; доказан источник и закрыт regression | verified locally: exact native-final provenance; terminal-only filter for new and persisted split finals green |
| A47.18 | default-session naming; creation worker owner | При включённом auto-name автоматически создаваемая default/standby session проходит тот же naming flow, что ручная session | verified locally: late refresh plus Generate/Load/Rename retry and production-order Starting FIFO tests green |
| A47.19 | postflight callback recovery; integration owner | Известный Telegram receipt завершает старый half-commit без повторного edit, воскрешения удалённой карточки или возврата на старую active session | verified locally: 6/6 live-derived cases drain on isolated copies; permanent missing-card and stale-select regressions green |
| A47.20 | Codex approval parsing; integration owner | Включённое автоподтверждение принимает одноразовый пункт 1, даже если Codex переносит маркер `(p)` второй опции на отдельную строку | verified locally: exact 1,823-byte live screen changed RED parser to GREEN; public corpus and flow regressions green |
| A47.R | integration owner | Focused GREEN, full `make check-full`, exact-SHA CI, install/restart и live postflight | corrected local `20260911-session-flow-reliability-recovery make check-full` GREEN; exact-SHA CI/deploy/postflight pending |

Зоны A47.2 и A47.3 не пересекаются по пакетам и выполняются параллельно.
Integration owner один меняет A47.1, объединяет результат, запускает полный gate
и выполняет release последовательно. Stop condition каждой зоны - доказанный
RED, минимальный GREEN и соседний package suite; дорогой внешний probe не нужен.

A47.8 подтверждён live для update `783531830`: Luna завершила обработку за
2.407 s, durable `prompt-status:preprocessed` был superseded, а следующий
`prompt-status:accepted` был suppressed уже открывшимся native surface. Между
завершением preprocessing и final не было Telegram edit. Исправление должно
ввести delivery barrier только для успешно подготовленного prompt: основной
provider не стартует, пока exact status не получил terminal delivery receipt
Ожидание bounded, но timeout, enqueue failure и unknown receipt не являются
разрешением запускать основной provider: durable input остаётся retryable до
подтверждённой публикации. Обычный card edit уже прикладывает актуальный
screenshot через тот же rich-message запрос.

Независимый review текущего WIP также подтвердил четыре race/restart края:
старый output lease не имеет автоматического wake после expiry; status stale
selection проверяется до `selectionMu`; active-session fence заканчивается до
физического Telegram edit; `Starting` исключён из archive fallback. Отдельно
обнаружены отсутствующий `file_size`, Codex+Claude Luna replacement edge и
слишком широкая трактовка любого `context.Canceled` как штатной остановки.

Focused GREEN: package suites и `-race` прошли для controller, flow, nodes,
durable output, document policy, Luna core/session и transport; 50 повторов
критичных controller/flow interleavings также прошли. Architecture и vet
проходят. Независимый review добавил защиту prompt-status от supersede, проверку
active для любого session-card edit и удаление внутреннего 3-second delivery
timeout. Новый пользовательский scope A47.14-A47.17 добавлен до release; полный
gate будет повторён только после него.

Финальный локальный gate `TMPDIR=/private/tmp VERSION=20260911-session-flow-reliability
GOCACHE=/private/tmp/bria-go-cache make check-full` прошёл полностью: policy,
links, source secret scan, formatting, architecture, все unit/integration, vet,
packaging, полный race и executable trio. Первый race-run выявил независимый
недетерминизм fixture: grandchild блокировался голым `select {}` и мог быть
завершён runtime как deadlock под общей нагрузкой. Timer-backed helper прошёл
100/100 целевых race-прогонов, после чего полный gate стал GREEN.

Первый postflight `1bd0a1d` обнаружил повторяющийся каждые 30 s
`telegram.status_delivery_failed`. В store было шесть coupled half-commit:
callback уже имел точный `receipt_confirmed`, status оставался `send_unknown`.
Две старые операции ссылались на уже удалённые карточки с legacy empty-page,
четыре были старыми `select_session`, после которых пользователь уже выбрал
другую active session. Recovery пытался заново применить эти старые проекции;
кроме того, проверка exact carrier revision перезаписывала ранее вычисленный
stale-флаг. Исправление сохраняет OR всех stale-причин и завершает известный
receipt без Telegram mutation и без восстановления устаревшей UI-проекции.

До corrected release активная Codex-сессия показала ещё один неподтверждённый
command approval при `auto_approve_commands=true`. Read-only capture точного
живого tmux-pane подтвердил новый перенос: закрывающая кавычка persistent-rule
оставалась в строке команды, а маркер `(p)` рендерился следующей строкой.
Парсер требовал суффикс `` ` (p) `` в одной физической строке и поэтому не
создавал `native.approval` вообще. Новый corpus fixture воспроизвёл RED;
нормализация только строк известной второй опции сохраняет точные heading,
selected one-shot, decline и footer gates и не разрешает persistent choice.
