# A48: UI latency and session metadata

## Договор задачи

- Результат P0: измерить и устранить рост задержки от Telegram callback tap до
  фактического обновления карточки для переключения сессий, создания новой
  сессии, браузера директорий, настроек и пагинации.
- Результат P1: не показывать созданную без контекста New-сессию в разделе
  `Фон`, добавить переименование ноды в настройках и показывать время последнего
  изменения сессии в формате `hh:mm:ss` в заголовке карточки.
- Дополнение P0: восстановить согласованный A44 flow активной карточки после
  переключения сессий и устранить расхождение в выборе carrier, страницы,
  `follow_latest` либо в позднем редактировании неактуальной карточки.
- Дополнение: повторно проверить production-путь автонейминга default-сессий;
  прежний regression test не является приёмкой, если live default остаётся без
  автоматически обновлённого имени.
- Дополнение: убрать lifecycle copy (`готова`, `в работе` и другие состояния)
  из заголовка активной карточки; сохранить label, node, CLI, model и `hh:mm:ss`.
- Источники: safe structured logs актуальной новой Bria после запуска
  `20260911-session-flow-reliability-recovery`, текущий код и публичные тестовые
  seams. Период live RCA - с 2026-09-11 16:32:45 MSK до конца проверки.
- Grain/population: один callback flow и одна физическая Telegram card mutation;
  все пять перечисленных UI-flow, все active/starting sessions и все nodes.
- Exclusions: payload пользовательских сообщений, ручные Telegram inputs,
  изменения runtime state/config/secrets и исторический `bria-legacy`.
- Writes: product code, tests и docs только текущего репозитория; после полной
  приёмки действует standing release в `origin/main` и restart только
  `gui/501/com.time4mind.bria.v2`.
- Приёмка: для каждого P0-flow зафиксирован callback-to-card timing либо явно
  доказана граница телеметрии, regression probe воспроизводит дефект до фикса и
  проходит после; P1 проверен через публичный UI seam; полный `make check-full`,
  exact-SHA CI, install/restart и live safe postflight проходят.

## Coverage map

| ID | Зона | Owner/write boundary | Stop criterion | Статус |
|---|---|---|---|---|
| A48.1 | P0 live timing inventory и root-cause synthesis | integration owner; read-only logs, затем точный production seam | пять flow классифицированы, bottleneck доказан, RED probe выбран | local verified; четыре physical taps pending |
| A48.1b | P0 active-card follow/pinned после session switch | read-only RCA owner, integration owner правит common flow | последний live-эпизод сопоставлен с A44 contract; carrier/page/follow и late-edit regression GREEN | local verified |
| A48.2 | Пустая New-сессия и `Фон` | отдельный owner после code map; session/card projection files | пустая foreground New не попадает в background list, непустая работает как раньше | local verified |
| A48.3 | Rename node в Settings | node/settings owner; только node settings package и его tests | текущий name показан, rename persisted, collision/invalid input fail closed | local verified |
| A48.4 | `hh:mm:ss` последнего изменения сессии | card/session metadata owner; renderer/model files и tests | заголовок использует persisted session change time, timezone определена тестом | local verified |
| A48.5 | Independent review | read-only reviewer | проверены race, ordering, latency и product contract | approved |
| A48.6 | Default-session auto-name live defect | отдельный naming owner; naming/standby files only | младшая модель получает strict Latin prompt; любой ответ вне ASCII A-Z/a-z/0-9 отклоняется; валидное имя применяется и обновляет карточку до окончания основной модели | local verified |
| A48.7 | Live `awaiting_recovery` при живом Codex terminal | recovery owner; supervision/session attachment tests and implementation | adapter/observer failure не оставляет живой terminal в устойчивом awaiting; unresolved inputs безопасно skip-ятся по согласованному recovery contract | local verified |
| A48.8 | Короткий заголовок карточки без readiness copy | integration owner; session header renderer и wire regression | lifecycle state отсутствует в header, model и время сохраняются | local verified |
| A48.R | Release | integration owner | full gate, exact SHA CI, verified install and live postflight | local gate verified; release pending |

Integration owner владеет synthesis, общими flow-файлами и выпуском. Owners не
редактируют пересекающиеся файлы; обнаруженное пересечение возвращается
integration owner до изменения. Дорогие внешние probes ограничены существующими
safe logs и identity-only postflight; ручной Telegram tap агент не создаёт.

## Локальные доказательства

- Live RCA 2026-09-11 с 16:32:45 MSK: callback ingress-to-receipt для выбора
  сессии 1.121-2.132 s; лишние inventory/history reads и stale-token rejection
  подтверждены безопасными structured logs без чтения payload.
- RED/GREEN seams: один inventory read на New/browser page, repeatable old
  navigation token, empty standby без `Фон`, node rename, persisted activity
  timestamp, immediate auto-name и recovery живого terminal.
- Автонейминг: prompt явно разрешает только `A-Z`, `a-z`, `0-9`; тест с ответом
  `Кнопка меню` получает invalid output, а `Fast name` сохраняется и вызывает
  refresh до завершения primary turn.
- Header regression: `default · Local · codex · gpt-5.6-sol · 17:10:37`
  сохраняет модель и московское время, но не содержит lifecycle copy `готова`
  или `не готова`; фоновые badges остаются без изменений.
- Exact-SHA Platform Matrix `34625153557` выявил два macOS края: queue-limit
  fixture не был изолирован от satellite preprocessing, а reaper мог убить
  adapter между stdout EOF и финальным безопасным stderr-классом. Первый тест
  теперь явно отключает preprocessing; второй имеет deterministic RED probe и
  bounded 100 ms drain. Дополнительный single-CPU stress выявил промежуток,
  когда durable session уже Ready, а controller ещё не установил live state:
  он теперь безопасно defers точный unsent input, а Ready-переход повторно будит
  очередь. Профильные race-повторы GREEN: runtime 100/100, queue-limit 50/50
  при `GOMAXPROCS=1`, оба ready-gap seam 100/100; architecture GREEN без
  расширения package budget.
- Independent review обнаружил и закрыл обход через третий фрагмент ответа:
  `Fast name Кириллица` теперь целиком отклоняется до усечения. Повторный review
  актуального diff завершён `APPROVE`.
- Явная навигация теперь атомарно сохраняет page plan, а read-only projection
  его не меняет. Pinned/latest/reopen stress GREEN 20/20; конфликтующий тест
  допускает только обязательное обновление `hh:mm:ss`, но не текста страницы.
- `VERSION=20260911-ui-latency-session-metadata make check-full` GREEN, включая
  policy, secret scan, architecture, unit/integration, vet, packaging, global
  race и executable trio acceptance. Первый source SHA `80192c6` получил
  GREEN Platform Matrix `34617434528`; Stage 1 `34617434877` выявил race, где
  durable `Starting` уже видна в inventory, но async creator ещё не положил её
  in-memory handle, поэтому немедленный выбор не закреплялся и первый FIFO input
  мог потеряться. Контроллер теперь синхронизирует валидную durable row в pending
  map при выборе; regression проходит 100 race-повторов, architecture GREEN.
- Второй failure Stage 1 в старом synthetic A25 native-exit test сохранил
  accepted receipt и model event, но не увидел диагностический stderr class.
  Текущий код не воспроизвёл его за 1000 последовательных race-повторов
  (70.8 s); assertions и runtime path не ослаблялись.
- При финальном локальном gate обнаружен независимый race-флейк Claude
  process-tree fixture: raw helper временно писал `ready` в тот же файл, из
  которого parent ожидал PID adapter. Сигналы разделены; точный тест проходит
  100 race-повторов. Финальный полный versioned gate исправленного дерева GREEN.
- Follow-up `26a36a1` отправлен в `origin/main`; Stage 1 `34620133344` и Platform
  Matrix `34620133235` завершились GREEN на точном SHA.
- Release preflight выявил старый mutable `golang:1.25-bookworm` в `Dockerfile`:
  `make release` корректно отказал до создания output. Официальный Docker
  registry 2026-09-11 вернул OCI index digest `sha256:3b4a1151...b36437`;
  `GO_IMAGE` закреплён полным digest, а supply-chain validation добавлен в
  `check-operational`, чтобы любой обычный full gate находил такой дефект до
  release. Follow-up exact-SHA CI и live postflight ещё не завершены.
- Первый локальный deploy подтвердил ещё две исторические ловушки: public
  `make release` требует внешний evidence manifest, а `service-control.sh`
  hard-code-ил legacy label `com.time4mind.bria` вместо plist label
  `com.time4mind.bria.v2`. Цель follow-up: явный signed `release-local`, полный
  signing preflight до cross-build и plist-derived fail-closed macOS service target с
  shell regression. Установленный runtime уже healthy; финальный bundle должен
  включать исправленные operational scripts и пройти штатный postflight.
- Финальный full gate выявил не product race, а не изолированный test fixture:
  exported `DIST_DIR` заставлял каждый временный repository-check повторно
  сканировать настоящие release archives и доводил `scripts` под `-race` до
  10-минутного timeout. Fixture теперь направляет `DIST_DIR` внутрь своего temp
  repo; сначала проверяется точный race-тест, полный gate повторяется только
  после его GREEN.
- Для New, browser, settings и pagination локально доказан один inventory/read
  path и regression latency seam. Физический callback-to-Telegram receipt для
  этих четырёх flow не создавался искусственно и остаётся на ближайшие обычные
  пользовательские нажатия; select-session измерен live выше.
