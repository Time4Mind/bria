# A42 - `awaiting_recovery` не является устойчивым состоянием

## Договор

- Результат: определить происхождение live-сессии
  `ab396f5b-2c57-4747-910e-08a79dfc5d62` и устранить сценарий, в котором
  основная сессия бессрочно остаётся `awaiting_recovery`.
- Целевой lifecycle: после bounded startup/recovery-прохода ранее открытая
  сессия с доказанно доступным terminal/provider state автоматически
  восстанавливается; при доказанном отсутствии восстанавливаемого процесса и
  thread она закрывается/архивируется. `awaiting_recovery` допустим только как
  краткое переходное состояние во время текущей попытки.
- Источники: live state, safe logs, native terminal/provider bindings и
  receipts, затем текущий код и public lifecycle tests. Период - от создания
  целевой сессии до postflight нового выпуска, timezone Europe/Moscow.
- Grain/population: одна основная Bria session и одна recovery decision на
  запуск; общий инвариант применяется ко всем поддерживаемым основным сессиям.
- Исключения: не редактировать live state вручную, не повторять accepted input,
  не создавать provider binding без доказательств, не раскрывать payload и не
  отправлять пользователю тестовые Telegram-сообщения.
- Разрешённые writes: код, тесты и документация текущего репозитория. После
  полного результата действует standing release authorization из `AGENTS.md`.
- Acceptance: public RED/GREEN воспроизводит устойчивый
  `awaiting_recovery`; recoverable branch приводит к `ready/running`, proven
  unrecoverable branch - к archived/closed; нет replay принятого input; focused
  race, полный gate, exact-SHA CI и live restart подтверждают отсутствие
  устойчивого `awaiting_recovery`. При активном satellite preprocessing restart
  также reconnect-ит прежний shared satellite либо каждый прежний per-session
  satellite с сохранением provider thread identity и очереди.

## Coverage map

| Зона | Owner | Результат | Stop criterion |
|---|---|---|---|
| Live provenance | integration owner | происхождение и физическое состояние `ab396f5b...` | state/log/process/receipt timeline без payload |
| Recovery state machine | recovery reviewer | точная ветка, оставляющая статус зависшим | falsifiable cause и минимальная граница фикса |
| Public regression seam | test reviewer | сценарии recoverable/unrecoverable и replay guard | RED-тесты на публичной lifecycle-границе |
| Satellite restart | integration owner | reconnect shared/per-session preprocess satellites | identity/queue RED/GREEN для обоих режимов |
| Integration/release | integration owner | единый фикс и физический postflight | full gate, CI, deploy, no stable awaiting |

## Todo

| ID | Обязательство | Acceptance probe | Статус | Evidence / граница |
|---|---|---|---|---|
| A42.1 | Идентифицировать live-сессию и причину состояния | bounded timeline state/log/native evidence | verified | `workdir7`, создана 10:42 MSK. Exact binding `01a08ac8...`, tmux PID 20903 и Codex PID 20909 живы; transcript продолжался около часа после перехода в awaiting. Native history знает accepted `783531720`, но не старый unknown `783531721`; строгая reconciliation после успешного attach возвращает ошибку и отцепляет observer. Dead terminal, потерянный binding и replay нового pending исключены. Payload не читался. |
| A42.2 | Устранить устойчивый `awaiting_recovery` | public RED/GREEN двух terminal branches | verified locally | Exact attach больше не откатывается из-за старого replay-fenced unknown без native receipt. `terminal_unavailable` проходит через bounded safe startup class и архивирует сессию; generic/transient ошибка не получает эту классификацию и остаётся retryable. |
| A42.3 | Не повторять старый accepted input | restart/recovery regression | verified locally | Старый unknown сохраняет фазу и не lease-ится повторно, но допускает более новый pending root. Reconciliation/selector/root tests по 10 повторов PASS. |
| A42.4 | Выпустить и проверить live | full gate, exact-SHA CI, service/state postflight | verified | Runtime `067a508f429189877c679b8e093aff90de111069` в `origin/main`; Stage 1 `34529896838` и Platform `34529897002` PASS. Установлен `20260910-terminal-recovery-satellite-v2`, PID 46682 running/sole process. `workdir7` осталась `ready`, сохранила provider thread `01a08ac8...`, generation 4→5 и живые tmux/Codex PID 20903/20909. Среди 4 активных сессий только `ready/running`, `awaiting_recovery` нет. |
| A42.5 | Проверить reconnect preprocess satellite при restart Bria | shared и per-session identity/queue restart tests | verified | Shared mode проверен live. Для физически сохранённого rollout exact-resume используется прежний binding; после main recovery `Reconcile` делает то же для каждого active primary в per-session mode, archived primary не запускается. Per-session не включался live, чтобы не менять настройку; exact identity/active-only/archived и FIFO покрыты automated restart/race tests. Durable journal сохранил 13 sessions/31 inputs; 8 sessions и 8 cards сохранены. |
| A42.6 | Не оставлять startup без сателлита, если прогретый пустой thread ещё не получил rollout | exact missing classification, fresh replacement; transient error preserves binding | verified | Live shared binding `01a08c7f...` не имел rollout. После двух bounded exact-resume попыток новый protocol frame классифицировал proven missing и создал replacement `01a08d25-93a5-7c10-9148-e813ce0dff64`; service log зафиксировал `ready` через 533 ms. Generic/network/auth failure по-прежнему не заменяет ID. После `ready` свежих failed/critical нет. |

## Локальная проверка перед выпуском

- `TMPDIR=/private/tmp/bria-a42-tests VERSION=20260910-terminal-recovery make check-full` - PASS: policy, formatting, architecture, все unit/integration, полный race, vet, operational packaging и executable trio.
- Первый диагностический gate с неканоническим macOS `TMPDIR=/var/...` воспроизвёл старое расхождение symlink-path в filesystem fixtures. Те же четыре пакета и затем весь gate прошли с каноническим `/private/tmp`; набор проверок не сокращался.
- После первого live restart основной recovery прошёл, но shared warmup дважды получил
  provider `start_failed`. Прямой metadata-only `thread/resume` без prompt вернул
  `no rollout found`; это доказанный пустой warm thread, а не network/auth сбой.
  Дополнительный focused plain/race и architecture gate для replacement-фикса PASS.
- `TMPDIR=/private/tmp/bria-a42-tests VERSION=20260910-terminal-recovery-satellite make check-full`
  PASS вне filesystem sandbox: policy, format, architecture, все unit/integration,
  native tmux tests, vet, packaging, полный race и executable trio. Контрольный
  запуск `internal/nativeadapter` подтвердил, что массовый первый сбой был
  sandbox-only и не воспроизводится в разрешённом terminal context.
- Первая process-boundary реализация пыталась передать классификацию через stderr
  внешнего adapter. Она не работала: штатный `KillCurrentTree` завершает adapter
  вместе с raw Codex раньше возврата в `main`. Исправление публикует узкий
  типизированный frame из `RunAdapter` до остановки дерева. Обычный helper-тест
  проверяет frame и класс ошибки; opt-in metadata-only тест с реальным Codex
  подтвердил сквозной `ErrResumeUnavailable` без отправки prompt.
- Первый повторный полный gate обнаружил независимую гонку в test-only ready
  evidence: helper создавал PID-файл до записи значения, и reader один раз увидел
  пустую строку. Runtime не затронут. Helper переведён на уже существующую
  атомарную публикацию `publishPID`; 100 plain и 20 race повторов PASS.
- Финальный candidate `VERSION=20260910-terminal-recovery-satellite-v2` прошёл
  полный `make check-full` вне filesystem sandbox: policy, links, secret scan,
  format, architecture, весь plain/integration набор, vet, operational packaging,
  полный race и executable trio.

## Release receipt 2026-09-11 Europe/Moscow

- Source и CI: `067a508f429189877c679b8e093aff90de111069` является exact
  `origin/main`; Stage 1 `34529896838` и Platform `34529897002` завершились
  `success` для этого SHA.
- Установка: `current -> releases/20260910-terminal-recovery-satellite-v2`,
  `previous -> releases/20260910-terminal-recovery-satellite`. SHA-256 тройки:
  `bria` `6e6cffe1...`, Codex adapter `1b644520...`, Claude adapter `d4db9e21...`;
  mode 755. Первый pointer-switch через `mv` был отклонён reread до restart:
  macOS последовал symlink-каталогу. Два созданных temporary symlink удалены,
  pointers безопасно переключены `ln -sfn`, hashes повторно совпали.
- Service: только `gui/501/com.time4mind.bria.v2`, runs 5, PID 46682 running;
  `bria version`, `check-config` и identity-only `check-telegram` PASS.
  Config `058fc7fd...`, settings `89aa3a55...`, plist `d23a49c1...` не изменились.
- Данные и lifecycle: 8 sessions/8 cards, journal 13 sessions/31 inputs.
  Active statuses: 3 ready, 1 running; stable awaiting отсутствует. Shared Luna
  replacement ready; critical events после нового `telegram.flow_ready` - 0.
