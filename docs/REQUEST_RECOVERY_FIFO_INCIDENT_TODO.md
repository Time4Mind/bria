# A51: active request recovery and FIFO incident

## Договор задачи

- Результат: установить фактический флоу двух последних запросов в активной
  сессии `KidAccess` и доказать, почему второй запрос не дошёл до Codex.
- Источники: live state, message journal, native acceptance receipt и safe logs
  установленной `20260912-performance-efficiency-v2`, затем текущий исходный
  код recovery и regression tests.
- Период: 2026-09-12 10:34-10:44, Europe/Moscow.
- Grain/population: два Telegram input `783532031` и `783532032` одной активной
  Codex-сессии; остальные сессии и запросы исключены.
- Writes: только этот диагностический контекст. State, config, provider process
  и пользовательские сообщения не изменять и не переотправлять.
- Приёмка: для каждого input сопоставлены ingress, preprocessing, provider
  acceptance, journal phase и видимый результат; причина прослежена до точной
  recovery-операции в исходниках.

## Обязательства и статус

| ID | Обязательство | Статус | Evidence |
|---|---|---|---|
| A51.1 | Разобрать текущий request flow | verified | `783532031` появился в карточке, прошёл Luna preprocessing, был принят Codex и породил commentary/tool events. |
| A51.2 | Объяснить, почему следующее сообщение не дошло | verified | `783532032` появился в карточке и прошёл preprocessing, но provider submit отсутствует; journal phase - `skipped`. |
| A51.3 | Найти failing boundary | verified | После provider failure generation сменилась `9 -> 10`; committed input-recovery cutoff `through_sequence=315` перевёл input sequences `308` и `311` в `skipped`. |
| A51.F | Исправление и live acceptance | not authorized | Диагностический запрос не разрешает изменение продукта. |

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

## Целевая граница будущего исправления

1. Уже принятый input с точным provider turn ID должен пройти reconciliation,
   а не безусловный skip.
2. Гарантированно неотправленный `pending` successor должен пережить recovery и
   остаться в FIFO новой generation.
3. События старой generation должны приниматься только при доказанной связи с
   сохранённым accepted turn; они не должны создавать ghost-flow после skip.
4. Acceptance требует RED/GREEN recovery test с accepted turn плюс pending
   successor и live-проверку нового запроса после реального reconnect.
