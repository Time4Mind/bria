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
| A42.4 | Выпустить и проверить live | full gate, exact-SHA CI, service/state postflight | pending | Без ручной правки state и тестового Telegram input. |
| A42.5 | Проверить reconnect preprocess satellite при restart Bria | shared и per-session identity/queue restart tests | verified locally | Shared warmup загружает прежний binding и exact-resume-ит один thread. После main recovery `Reconcile` exact-resume-ит прежний thread каждого active primary; archived primary не запускается. Очередь остаётся в durable main journal. Новые restart tests по 10 повторов и full race PASS. |

## Локальная проверка перед выпуском

- `TMPDIR=/private/tmp/bria-a42-tests VERSION=20260910-terminal-recovery make check-full` - PASS: policy, formatting, architecture, все unit/integration, полный race, vet, operational packaging и executable trio.
- Первый диагностический gate с неканоническим macOS `TMPDIR=/var/...` воспроизвёл старое расхождение symlink-path в filesystem fixtures. Те же четыре пакета и затем весь gate прошли с каноническим `/private/tmp`; набор проверок не сокращался.
