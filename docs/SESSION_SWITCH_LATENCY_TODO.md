# A52: session switch latency

## Договор задачи

- Результат: сократить видимую задержку `tap -> обновлённая карточка` при
  переключении сессии до минимально достижимой без потери порядка, защиты от
  дублей и восстановления после сбоя.
- Источники: safe detailed logs установленной revision `55cbf823`, текущий код
  callback/delivery/state paths, локальный Python-проект как сравнительный
  ориентир при обнаружении его точного пути, затем regression tests.
- Период: свежие callbacks после deploy 2026-09-12 12:42:25 MSK; timezone -
  Europe/Moscow.
- Grain/population: один callback `select_session` от приёма Telegram до
  успешного edit карточки; минимум три свежих переключения. Настройки и другие
  экраны входят только при доказанном общем bottleneck.
- Исключения: содержимое пользовательских сообщений, ручные Telegram inputs,
  изменение state/config/secrets.
- Writes: product/tests/docs текущего репозитория и standing release sequence.
- Приёмка: RED/GREEN на подтверждённой внутренней причине, профильный timing/work
  probe, полный `make check-full`, exact-SHA CI, signed local release, restart
  только `gui/501/com.time4mind.bria.v2` и live postflight. Финальная оценка
  субъективной скорости требует нового пользовательского tap.

## Обязательства

| ID | Обязательство | Статус | Evidence |
|---|---|---|---|
| A52.1 | Разложить свежую задержку по стадиям | verified | Три `select_session`: ingress -> edit success `1021/1337/1440 ms`; до edit `800-901 ms`, из них controller `226-305 ms`, Telegram edit `187-538 ms`. Ошибок и polling delay нет. |
| A52.2 | Сопоставить с быстрым Python-флоу | verified | Исторический ccbot: одна crash-safe state write до edit и один Telegram edit; новая Bria выполняла около 14 atomic file commits до edit. |
| A52.3 | Исправить корневую причину без потери гарантий | partial; iteration 2 active | Уже fenced `CallbackPrepared` доставляется без второго status-outbox и без outer checkpoint перед edit; transcript/page/session-list читаются одним joined snapshot. RED доказал, что раньше edit ждал outer checkpoint; GREEN - direct edit начинается до него. Stale definitely-unsent callback durably завершается без блокировки очереди; send-unknown сохраняет `ErrDeliveryUnknown`. Live улучшение недостаточно, поэтому оставшиеся последовательные pre-edit writes исследуются отдельно. |
| A52.4 | Выпустить и проверить новую версию | deployed; acceptance failed | Source commit `0552914` в `origin/main`; Stage 1 `34688585550` и Platform Matrix `34688585553` GREEN. Signed release `20260912-session-switch-latency` установлен, state/journal и Telegram postflight GREEN. Четыре реальных tap показали до `1.33 s`; Артём не принял задержку. |
| A52.5 | Убрать оставшиеся storage/ack commits интерактивного пути | implemented; full gate and review GREEN | Repeatable durable callback больше не переписывает registry; RED/GREEN подтверждает `CallbackClaimed` как ack restart-fence без acknowledgement record. Generation cache убирает повторный parse неизменного state; healthy scheduler не делает второй no-op fsync. Finalized history `256 -> 64`, при этом все non-finalized и coupled records обходят cap. Append-only WAL отклонён из-за небезопасного rollback. Полный `VERSION=20260912-session-switch-latency-v3 make check-full` и независимое review завершились успешно. |
| A52.6 | Выпустить iteration 2 и повторить live acceptance | release pending | Локальный полный gate и независимое review GREEN. Остались exact-SHA CI, signed release, postflight и новые реальные tap. |

## Проверенная причина

Python ccbot и новая Bria делают одинаковое число сетевых операций, но Bria
повторно журналировала один callback в трёх слоях: callback operation,
coordinator outbound checkpoint и generic status operation. Параллельно card
projection трижды перечитывал общий `state.json` размером 2.8 MB. Предыдущий
фикс убрал только N+1 чтения standby-сессий и поэтому не мог заметно изменить
весь пользовательский latency.

## Реализованная граница

1. Callback effect и prepared output по-прежнему фиксируются до внешнего edit.
2. Перед edit остаётся единственный `CallbackSendUnknown`; неоднозначный
   transport result автоматически не повторяется.
3. После receipt выполняются binding/state commit и callback commit.
4. Outer coordinator checkpoint сохраняется один раз после видимого edit;
   crash до него безопасно replay-ится через committed callback без дубля.
5. Card projection получает transcript, page intent, sessions и empty-close
   evidence из одного storage snapshot; fallback для иных store сохранён.
6. Stale select, остановленный до transport, атомарно переходит в
   `effect_resolved`; restart не повторяет его и не блокирует следующие updates.
7. Ошибка transport после `CallbackSendUnknown` сохраняет одновременно классы
   callback unknown и coordinator delivery unknown.

## Coverage map

| Зона | Owner | Результат | Stop criterion |
|---|---|---|---|
| Fresh live timing | Integration owner | Полный critical path трёх tap | Каждая существенная пауза отнесена к boundary |
| Callback semantics | Integration owner | Убрать только дублирующие ack/registry commits; `internal/telegramflow`, `internal/telegrampipeline`, `internal/callbackregistry`, `internal/telegrambridge` | RED/GREEN доказывает сохранение restart/replay fences и bounded work до edit |
| Callback storage | Agent WAL | Append-only durability либо доказанный меньший безопасный вариант; только `internal/telegramops` и его тесты | Torn-write, reopen, CAS, compaction и retention GREEN |
| Session storage | Agent cache | Сократить повторные parse/reload; только `internal/storage` и его тесты | External replacement, failed write и work-count GREEN |
| Telegram/Python comparison | Agent transport | Отделить scheduler/HTTP/ack contention и подтвердить предел внешней задержки; read-only | Найден конкретный управляемый bottleneck либо доказана внешняя граница |
| Mutation scheduler | Agent scheduler | Не выполнять второй atomic write при неизменившемся healthy state; только `internal/mutationscheduler` и его тесты | Success no-op write-count и все failure/restart transitions GREEN |

Integration owner объединяет evidence и единолично выполняет изменения Git и
release. Агенты работают read-only в непересекающихся областях.

## Release receipt

- Полный локальный gate `VERSION=20260912-session-switch-latency-v2 make
  check-full` завершился с exit code `0`.
- `origin/main` и source commit совпали на
  `0552914ec2437ceaa738bf2a0e96bc4b4887a174`; обязательные GitHub workflows
  `34688585550` и `34688585553` завершились успешно.
- Подписанный bundle `20260912-session-switch-latency` содержит этот exact
  revision; release verification и `postflight.sh --telegram` GREEN.
- После restart сервис `gui/501/com.time4mind.bria.v2` перешёл `runs 19 -> 20`,
  PID `70226 -> 42262`; один parent использует новый `current`, native adapters
  запущены из той же release directory.
- Хэши `config.json` и `state.json.settings.json` до/после совпали; state и
  message journal совместимы. В state остались `6 ready` и `13 archived`,
  `awaiting recovery` нет. После startup зафиксированы `flow_ready` и готовый
  Luna preprocessing satellite, новых critical errors нет.
- Первый post-change `tap -> edit` измерен реальными переключениями; acceptance
  не пройден. Искусственный Telegram input не создавался.

## Live acceptance после первого фикса

Четыре реальных `select_session` после startup дали `ingress -> edit success`
`1333/778/1163/722 ms`, среднее `999 ms`. До начала Telegram edit прошло
`509/464/504/466 ms`, среднее `486 ms`; transport занял `823/313/658/255 ms`.
По сравнению с baseline средний внутренний pre-edit уменьшился примерно на
`359 ms` (`42%`), а весь измеряемый путь - на `267 ms` (`21%`). Пользователь
всё ещё видит задержку больше секунды; acceptance не пройден. Итерация 2
разбирает оставшиеся callback ack/claim/effect/output writes и их сериализацию.

## Итерация 2

1. Валидный callback сначала сохраняет `CallbackClaimed`, затем запускает
   Telegram acknowledgement без отдельной acknowledgement-записи. Restart и
   concurrent duplicate по-прежнему упираются в operation identity/CAS;
   rejected и stale callback сохраняют прежний durable acknowledgement path.
2. Repeatable page/session/archive navigation не записывает claim в отдельный
   presentation registry: operation ledger уже является exact-update fence.
   One-shot approval, recovery, retry и interaction callbacks не изменены.
3. `SessionStore` сравнивает inode/size/mtime/mode основного файла и activity
   sidecar. Неизменившийся state не декодируется повторно; успешная атомарная
   запись публикует новое поколение только после physical verification.
4. Успешный Telegram mutation completion не переписывает scheduler state, если
   он совпадает с уже сохранённым состоянием acquire. Ошибки, cooldown, auth,
   rate-limit и concurrent changes по-прежнему записываются.
5. Неизменившийся secure `card-activity` sidecar больше не заменяется; изменение
   activity или неправильные permissions по-прежнему приводят к atomic rewrite.
6. Finalized callback/status/ack history ограничена `64` вместо `256`; текущий
   физический snapshot после следующей mutation ожидаемо уменьшается примерно
   с `820` до `188 KB`. Все non-finalized записи и committed counterpart,
   требуемый незавершённой парой, сохраняются сверх cap.
7. WAL-прототип удалён: установленный предыдущий бинарник `0552914` игнорирует
   sidecar, а rollback check не валидирует callback ledger. Он мог увидеть
   старую фазу `claimed` и повторить effect; это нарушает критерий надёжности.

RED/GREEN: отдельный ack record `1 -> 0`, повторные full state reloads `3 -> 0`,
healthy scheduler persistence calls `2 -> 1`, finalized namespace history
`257 -> 65` с сохранением active/coupled записей, identical activity rewrites
`1 -> 0`. Focused package tests и общий focused `-race` GREEN. Architecture
registry обновлён ровно под добавленные обязанности; `check-architecture`,
`check-policy` и `release-supply-chain` GREEN. Live latency новой версии ещё не
проверена.
