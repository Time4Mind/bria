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
| A52.3 | Исправить корневую причину без потери гарантий | implemented, focused/race/review GREEN | Уже fenced `CallbackPrepared` доставляется без второго status-outbox и без outer checkpoint перед edit; transcript/page/session-list читаются одним joined snapshot. RED доказал, что раньше edit ждал outer checkpoint; GREEN - direct edit начинается до него. Stale definitely-unsent callback durably завершается без блокировки очереди; send-unknown сохраняет `ErrDeliveryUnknown`. Три независимых review - APPROVE. |
| A52.4 | Выпустить и проверить новую версию | deployed; live tap pending | Source commit `0552914` в `origin/main`; Stage 1 `34688585550` и Platform Matrix `34688585553` GREEN. Signed release `20260912-session-switch-latency` установлен, state/journal и Telegram postflight GREEN. Остался пользовательский tap для post-change latency. |

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
| Callback durability | Agent A | Карта обязательных и лишних sync writes | Точный call graph и безопасная оптимизация |
| Python comparison | Agent B | Отличия fast path без чтения payload | Найден точный проект либо зафиксировано отсутствие |
| State/projection | Agent C | Остаточные reload/serialization costs | Счётчик операций и кандидат regression seam |

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
- Непроверенная граница: post-change `tap -> edit` будет измерен по следующему
  реальному пользовательскому переключению; искусственный Telegram input не
  создавался.
