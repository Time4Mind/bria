# A55 - настройки скрина карточки

## Договор задачи

- Результат: в разделе «Кнопки сессии» остаётся только переключатель «Скрин»;
  объём terminal capture и профиль PNG находятся в «Содержимое карточки» и
  применяются к действующей native-сессии без её пересоздания.
- Источник: указание Артёма от 2026-09-12, текущий код, persisted settings и
  безопасные структурированные логи новой Bria.
- Период и часовой пояс: текущая установленная версия на 2026-09-12,
  Europe/Moscow.
- Единица результата: каждый видимый settings field/button, его signed callback,
  persisted value и terminal screenshot активной сессии.
- Включено: Telegram settings view/routing, runtime capture boundary, тесты,
  документация и обычный release в `origin/main` с перезапуском
  `gui/501/com.time4mind.bria.v2`.
- Исключено: ручное изменение live settings/state, исходящие Telegram-сообщения,
  Bria Legacy, состав transcript и частота обновления карточки.
- Acceptance: «Кнопки сессии» содержит только «Скрин» и «Назад»; два параметра
  скрина видны и работают в «Содержимое карточки»; увеличение лимита влияет на
  уже работающую сессию; stale signed callbacks остаются совместимыми;
  focused RED/GREEN, полный `make check-full`, exact-SHA CI, signed install и
  read-only service postflight проходят при сохранённых config/settings/state.

## Карта покрытия

| ID | Зона | Владелец | Критерий остановки |
|---|---|---|---|
| A55.1 | UI и category routing | integration owner | публичный view test RED -> GREEN, в разделе кнопок нет лишних controls |
| A55.2 | Значение и runtime-потребитель настроек | read-only investigator + integration owner | каждое поле сопоставлено persisted value; действующая сессия получает максимальный безопасный snapshot, renderer применяет выбранный live limit |
| A55.3 | Совместимость callback и документация | integration owner | старые authenticated actions принимаются и возвращают правильную категорию; docs соответствуют UI |
| A55.4 | Независимый review и release | read-only reviewer + integration owner | diff review без открытых findings; full gate, CI, install и postflight GREEN |

Дорогих внешних probes нет. Telegram writes не выполняются; пользовательская
проверка реальным tap остаётся внешней границей. Владелец объединения - текущая
Codex-сессия.

## Статус

| ID | Состояние | Доказательство |
|---|---|---|
| A55.1 | done locally | view RED -> GREEN: в «Кнопки сессии» только «Скрин»/«Назад», capture/profile находятся в «Содержимое карточки» |
| A55.2 | done locally | live log: `settings_screen_capture_limit` упал на runtime effect fence в 20:30:33 MSK; exact public adapter RED -> GREEN; final sanitized runtimefactory CommandSpec содержит один trusted 86 KiB, renderer применяет live preference |
| A55.3 | done locally | wire action IDs сохранены; category routing старых capture/profile callbacks ведёт в «Содержимое карточки»; CONFIGURATION обновлён |
| A55.4 | done locally; release pending | focused RED/GREEN и affected `-race` GREEN; независимый review после исправления high finding - `approve`; `VERSION=20260912-session-screen-settings make check-full` GREEN |

## Проверенная причина

- `Screen` был глобальным включателем PNG в карточке, а не отдельной
  разновидностью изображения.
- `Изображение Screen` меняло только профиль PNG; неудачное название и раздел
  смешивали его с видимостью кнопки.
- Нажатие `Размер захвата` 2026-09-12 в 20:30:33 MSK прошло signed pipeline,
  но `telegramruntimecomposition.callbackEffectForAction` не содержал action и
  отклонил план как `operation_failed`; persisted revision не изменилась.
- Даже после исправления callback увеличение лимита было бы ограничено старым
  startup value внутри adapter. Теперь `runtimefactory` после очистки родительского
  environment добавляет в final adapter `CommandSpec` bounded максимум 86 KiB,
  а существующий screenshot renderer применяет актуальный persisted limit.
- `telegramsettingsview` сохранил одну ответственность; её защищённый budget
  поднят с 350 до 375 production lines, текущий размер - 357 строк.
- Первый полный race-run встретил несвязанный scheduling-failure в
  `interactionflow`; точный тест и весь пакет затем прошли по 20/20, а свежий
  полный gate прошёл полностью.
