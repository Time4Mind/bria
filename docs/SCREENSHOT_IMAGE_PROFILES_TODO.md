# A53 - профили изображения терминального скриншота

## Договор задачи

- Результат: глобальная настройка Bria выбирает ровно один из трёх профилей
  терминального PNG: `как сейчас` (100%, полная палитра), `100% / 8 цветов`
  или `75% / 8 цветов`; дефолт - `100% / 8 цветов`.
- Источник требований: указание Артёма от 2026-09-12 Europe/Moscow и текущий
  код новой Bria в этом репозитории.
- Единица результата: каждый новый terminal screenshot активной native-сессии;
  существующие settings без нового поля получают профиль `100% / 8 цветов`.
- Включено: typed settings/codec/UI/action flow, renderer, screenshot cache
  identity/invalidation, тесты, конфигурационная документация и обычный выпуск
  в `origin/main` с перезапуском `gui/501/com.time4mind.bria.v2`.
- Исключено: объём захватываемого текста `screen_capture_limit_kib`, частота
  обновления карточки, состав текста, другие изображения и изменение live
  settings пользователя во время postflight.
- Acceptance: RED/GREEN через публичные seams; три профиля циклически доступны
  в Telegram settings и устойчиво сохраняются; PNG профилей имеет ожидаемые
  размеры и не более восьми используемых цветов там, где это задано; изменение
  профиля не возвращает старый cached PNG/file ID; legacy settings мигрируют в
  `100% / 8 цветов`; `make check-full`, exact-SHA CI, signed local install и service
  postflight GREEN при сохранённых config/settings/state/journal.

## Карта покрытия

| ID | Зона | Владелец | Файлы/пакеты | Критерий остановки |
|---|---|---|---|---|
| A53.S | Настройка, codec и Telegram UI/action | settings worker | `internal/settings*`, `internal/settingscapability`, `internal/settingsport`, `internal/telegramsettings*`, typed callback/action plumbing | focused tests демонстрируют RED до кода и GREEN после; legacy default подтверждён |
| A53.R | Рендер и PNG profiles | renderer worker | `internal/nativerender` | independent PNG config/color-count tests RED -> GREEN; текущий профиль byte/geometry-compatible |
| A53.C | Cache/composition, интеграция, docs и release | integration owner | `internal/nativescreencache`, `internal/screen`, `internal/screenproduction`, `internal/singlemachinecomposition`, `docs`, release | cache identity test RED -> GREEN, все отчёты проверены в общей папке, full gate/CI/deploy/postflight GREEN |
| A53.A | Architecture gate review | read-only reviewer | package boundaries and `scripts/check_repo.go` evidence only | confirms the smallest justified split/budget changes and reports any boundary defect |

Зависимость: A53.C подключает уже определённое A53.S поле и A53.R option после
готовности их публичных границ; A53.A проверяет обнаруженные size-gate failures
read-only параллельно с интеграцией. Пересекающихся write-зон нет. Дорогой внешний
источник не нужен; единственная внешняя последовательность - bounded release по
standing authorization после полного локального gate. Владелец объединения -
текущая Codex-сессия.

## Статус

| ID | Состояние | Доказательство |
|---|---|---|
| A53.S | done locally | RED на отсутствующих field/action/UI; focused и affected `-race` GREEN; `full_8 -> compact_8 -> current` сохраняется и перечитывается |
| A53.R | done locally | current byte-compatible; Full8 100%/8 цветов; Compact8 75%/8 цветов; deterministic tests и real-PNG visual QA GREEN |
| A53.C | done locally; release pending | cache/profile setting-to-renderer tests RED -> GREEN; старый file ID не переиспользуется; `VERSION=20260912-screenshot-image-profiles make check-full` GREEN |
| A53.A | done | `pngprofile` выделен отдельно; package policies и защитные tests GREEN; independent review подтвердил bounded split |

## Проверенный локальный результат

- Реальный terminal PNG: `full_8` - `1472x942`, ровно 8 используемых цветов,
  `29 896` bytes; `compact_8` - `1104x706`, ровно 8 цветов. Оба варианта
  визуально читаемы, исходное terminal content сохранено.
- Representative transform benchmark на Apple M3 Pro: `full_8` около `47.3 ms`,
  `compact_8` около `55.8 ms`; обработка выполняется только для нового snapshot
  или изменённого профиля, а подтверждённый exact PNG использует Telegram file ID.
- Первый full gate корректно сделал RED три cmd/bria fixture, которые ожидали
  старый default current PNG. Ожидание обновлено на независимый Full8 render;
  три targeted сценария и повторный полный gate GREEN.
- Финальный независимый read-only review всего `git diff HEAD`: findings всех
  severity отсутствуют, verdict `approve`.
