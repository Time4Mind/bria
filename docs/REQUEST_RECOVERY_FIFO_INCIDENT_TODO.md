# A51: active request recovery and FIFO incident

## Договор задачи

- Результат: исправить потерю запросов при recovery, свежий непринятый запрос,
  ошибки открытия разделов «Настройки», «CLI» и «Создание сессии», а также
  чрезмерную задержку переключения сессий и проверить смежные callback-флоу.
- Источники: live state, message journal, native acceptance receipt и safe logs
  установленной `20260912-performance-efficiency-v2`, затем текущий исходный
  код recovery и regression tests.
- Период: 2026-09-12 10:34-11:27, Europe/Moscow.
- Grain/population: Telegram inputs `783532031`, `783532032`, `783532060` и
  `783532061` активной Codex-сессии; callback открытия settings categories 7/8;
  семь переключений сессий и три callback пагинации. Остальные сессии исключены.
- Writes: product/tests/docs текущего репозитория и standing release; state,
  config и пользовательские сообщения не изменять и не переотправлять.
- Приёмка: RED/GREEN regression на каждой подтверждённой границе, полный
  `make check-full`, exact-SHA CI, установка и штатный перезапуск только
  `gui/501/com.time4mind.bria.v2`, затем live postflight без ручного input.

## Обязательства и статус

| ID | Обязательство | Статус | Evidence |
|---|---|---|---|
| A51.1 | Разобрать текущий request flow | verified | `783532031` появился в карточке, прошёл Luna preprocessing, был принят Codex и породил commentary/tool events. |
| A51.2 | Объяснить, почему следующее сообщение не дошло | verified | `783532032` появился в карточке и прошёл preprocessing, но provider submit отсутствует; journal phase - `skipped`. |
| A51.3 | Найти failing boundary | verified | После provider failure generation сменилась `9 -> 10`; committed input-recovery cutoff `through_sequence=315` перевёл input sequences `308` и `311` в `skipped`. |
| A51.4 | Свежий непринятый запрос активной сессии | released, live input pending | `783532061`: Codex записал `task_started` через 0.5 s, но user transcript из-за compaction появился только через 63.7 s; прежний adapter завершал submit через 20.072 s. RED/GREEN использует `task_started.turn_id` только как provisional liveness без receipt и снимает ложный timeout; durable acceptance возникает лишь после exact text match. Exact attach отдельно сохраняет accepted и definitely-unsent pending. |
| A51.5 | Ошибка открытия «Настройки», «CLI», «Создание сессии» | released, live tap pending | Category 7 не имела wire mapping `settings_rename_node`; category 8 не имела callback-token/presenter mapping `settings_auto_approve_commands`. Оба action проходят focused signed-boundary tests и полный gate. |
| A51.6 | Задержка переключения сессий и смежных callback-флоу | released, live latency pending | До исправления card projection выполнял 12 reload файла 2.7 MB, включая семь N+1 standby reads. Теперь список сессий и empty eligibility читаются одним snapshot; regression требует ровно один batch read независимо от числа standby. Пользовательский tap после deploy остаётся live acceptance. |
| A51.F | Исправление, выпуск и live acceptance | verified except user input/tap | Source commit `2b982bb` прошёл local `check-full`, Stage 1 `34686134830` и Platform Matrix `34686134814`; signed release установлен, service `runs 17 -> 18`, PID `44095 -> 31751`, config/settings сохранены, state/journal compatible. Финальная receipt-only revision выпускается как `-v2`. |

## Проверенная причина

`BeginInputRecovery` фиксирует cutoff на текущем `next_sequence`. После успешного
reconnect `CommitInputRecoverySkip` переводит в `skipped` каждый unresolved input
до cutoff: `pending`, `accepted`, `failed` и `unknown`. В этом инциденте под
cutoff попали одновременно:

1. `783532031` - уже принятый Codex запрос с точным native turn ID и живыми
   событиями;
2. `783532032` - следующий запрос, который ещё не отправлялся provider-у.

Поэтому первый turn продолжил выдавать события, хотя durable journal уже считал
его пропущенным, а второй остался только текстом в карточке и больше не мог быть
выдан FIFO-worker-у. Это не штатное ожидание в очереди, а ошибочная потеря
custody при recovery.

## Реализованная граница исправления

1. Уже принятый input с точным provider turn ID должен пройти reconciliation,
   а не безусловный skip.
2. Гарантированно неотправленный `pending` successor должен пережить recovery и
   остаться в FIFO новой generation.
3. События старой generation должны приниматься только при доказанной связи с
   сохранённым accepted turn; они не должны создавать ghost-flow после skip.
4. Полный выпуск и live postflight остаются частью A51.F; новый пользовательский
   input после реального reconnect не отправляется автоматически из проверки.

## Дополнительная первопричина свежего запроса

Нативный transcript текущей Codex-сессии доказал, что запрос не был отвергнут:
`task_started` с exact turn ID появился примерно через `0.5 s` после отправки.
Во время compaction запись user message задержалась примерно до `63.7 s`, а
adapter считал только её подтверждением и завершал submit по фиксированному
таймауту `20 s`. Теперь `task_started` после baseline лишь временно доказывает,
что CLI продолжил работу, и отключает ложный timeout без receipt. Durable
acceptance по-прежнему требует точного совпадения user text; событие без exact
turn ID игнорируется, mismatched text не принимается. Если provisional turn
завершился без exact match, Bria снимает его и запускает новый bounded `20 s`
window: следующий queued turn может стартовать, а вечного зависания не возникает.

## Архитектурный и тестовый gate

- Размеры четырёх затронутых coherent responsibilities выросли в пределах
  принятого шага бюджета: messagejournal `1725 -> 1800`, nativeadapter
  `850 -> 875`, storage `1900 -> 1950`, telegramcontroller `5800 -> 5850`.
  Machine-policy tests изменены вместе с caps; фактические размеры остаются
  ниже новых лимитов.
- Focused RED/GREEN покрывают delayed user transcript после provisional task start,
  attach/skip recovery semantics, signed settings actions и один batch snapshot
  для семи standby-сессий.
- Полный локальный gate вне sandbox завершился GREEN: policy, format,
  architecture, tests, vet, operational contracts, global race и executable
  trio. Release/CI/live postflight ещё не зафиксированы.
- Три независимых post-implementation review закрыты APPROVE; два найденных
  риска provisional acceptance исправлены до финального gate.

## Release receipt

- Source commit: `2b982bb55c1bc8ef5fac64dcd63c711a04324ac0`; origin/main перечитан после push.
- GitHub: Stage 1 `34686134830` GREEN, Platform Matrix `34686134814` GREEN.
- Первый signed release `20260912-request-recovery-callback-latency` установлен;
  service `gui/501/com.time4mind.bria.v2` перешёл `runs 17 -> 18`, PID
  `44095 -> 31751`, postflight GREEN.
- Config и settings hashes не изменились; `check-state` подтвердил совместимость
  state и message journal. После startup активная `KidAccess` имеет status
  `ready`, awaiting-recovery сессий нет, shared Luna satellite готов за `628 ms`,
  новых critical/service errors нет.
- Реальный новый Telegram input и пользовательские taps не создавались в
  postflight; их latency/acceptance остаётся внешней границей проверки.
