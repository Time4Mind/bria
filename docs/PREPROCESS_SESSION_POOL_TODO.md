# A40 - фоновая сессия препроцессинга

## Договор

- Результат: Bria поддерживает одну скрытую готовую сессию Luna/дешёвой
  модели. Передача запроса атомарно занимает её и немедленно запускает замену.
- Единица: один запрос на одну provider session; контекст запросов не смешивается.
- Принятие: старая сессия закрывается только после durable `PersistPrepared`.
  Ошибка модели принимает сохранённый fallback с исходным текстом.
- Границы: технические сессии не входят в пользовательский каталог, карточки и
  историю. Очередь bounded, startup/shutdown/config invalidation не оставляют
  процессы и сохранённые provider sessions без владельца.
- Выпуск: после полного `make check-full` автоматически commit, push, exact-SHA
  CI и перезапуск только `gui/501/com.time4mind.bria.v2` по standing authorization.

## Coverage map

| Зона | Owner | Результат | Статус |
|---|---|---|---|
| Pool и provider lifecycle | integration owner | start/ready/lease/replace/close | implemented |
| Durable acceptance | integration owner | закрытие строго после persistence | implemented |
| Provider transport | Newton, read-only | app-server и границы процесса | reviewed |
| Concurrency tests | Huygens, read-only | race/failure matrix | reviewed |
| Composition/logging/docs | Herschel, read-only | startup/shutdown/invalidation wiring | reviewed |

## Todo

| ID | Обязательство | Acceptance probe | Статус | Evidence / граница |
|---|---|---|---|---|
| A40.1 | Готовая скрытая сессия после старта | тест Ready до первого запроса | verified | внешний adapter сообщил `ready`; post-fix live Luna probe прошёл за 6.12 сек |
| A40.2 | Немедленная замена после lease | детерминированный ordering test | verified | replacement стартует до ответа занятой сессии |
| A40.3 | Close только после durable acceptance | controller regression test | verified | completion вызывается после `PersistPrepared` |
| A40.4 | Без смешения контекста и гонок | FIFO/concurrency/race tests | verified | один request на thread, Busy+Ready максимум 2 |
| A40.5 | Restart/config/failure cleanup | lifecycle tests и safe logs | verified | generation invalidation, shutdown cleanup, `close_failed` |
| A40.6 | Полный выпуск и postflight | full gate, CI, hashes, service health | pending | live synthetic Telegram input не отправлять |

## Проверенные исходные факты

- Старая реализация `promptpreprocesscommand.Warmup` выполняла отдельный
  `codex exec --ephemeral` и завершалась, поэтому готовой сессии не было.
- Новая реализация запускает отдельный process group `bria-codex-adapter` с
  `codex app-server` и ephemeral thread в read-only sandbox. Thread принимает
  ровно один запрос и не попадает в пользовательский каталог Bria или
  сохранённые Codex sessions.
- Durable результат фиксируется в `telegramturnhelpers.PersistPrepared` до
  provider handoff; это и есть граница принятия ответа препроцессинга.
- После lease сразу запускается новый app-server. Старый закрывается после
  durable acceptance; повтор того же lease до acceptance получает кэшированный
  результат без второго модельного вызова.
- При недоступном Codex остаётся stateless Claude Haiku fallback со статусом
  `degraded`, а не ложным `ready`: текущий Claude adapter не даёт проверяемой
  гарантии модели и готовности до запроса.
- Startup-read отменяется остановкой отдельной process group. Зависший close
  ограничен deadline и kill/wait; тестовый host остаётся жив после cleanup.
- App-server подтверждает принятие turn с requested Luna, но не возвращает
  независимый receipt фактически использованной модели; `model_evidence` пуст.
