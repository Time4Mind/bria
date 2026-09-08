# Конфигурация Bria и запуск Codex

Проверено по исходникам текущих Bria и CCBot 2026-09-08. Это описание текущей
рабочей версии, не свидетельство установки или перезапуска рабочего бота.
Конфиги с секретами и переменные работающего CCBot не читались: ниже описаны
его исходные defaults и правила применения overrides, а не неизвестные runtime values.

## Три разных слоя

| Слой | Где задаётся | За что отвечает |
| --- | --- | --- |
| Конфигурация процесса Bria | `bria run --config /absolute/path/config.json` | Владелец, бот, state path, доступные providers, executable/argv, инфраструктура |
| Настройки пользователя Bria | `<state_path>.settings.json`, меню «Настройки» | Интерфейс, очереди/срок жизни, preprocessing, standby, `auto_approve_commands` |
| Настройки самого Codex | Provider-owned config/CLI | Модель, reasoning effort, tools/MCP и прочие настройки провайдера |

Эти JSON-документы не взаимозаменяемы. Поле `auto_approve_commands` не является
полем главного `config.json`. `CODEX_COMMAND` и `CODEX_FLAGS` также не являются
настройками Bria и не импортируются из CCBot.

## Главный config.json

Минимальная single-machine форма без поля `version` поддерживается как
совместимый формат `combined`. Полный валидируемый шаблон находится в
[examples/bria-single-machine.json](examples/bria-single-machine.json).
Providers в шаблоне отключены, а пути и ID являются placeholders. Перед
реальным запуском требуется подставить свои значения; этот пример не даёт
разрешения запускать Telegram loop.

| Поле | Контракт |
| --- | --- |
| `owner_user_id`, `private_chat_id` | Положительные ID единственного владельца и его личного чата |
| `bot_username` | Имя бота без `@`, заканчивается на `bot` |
| `state_path` | Абсолютный путь основного durable state; настройки будут рядом с суффиксом `.settings.json` |
| `telegram_token` | Ровно один reference: `env_var` либо абсолютный `secret_file`; не сам токен |
| `callback_key.secret_file` | Отдельный абсолютный путь к секретному ключу callback |
| `providers.codex`, `providers.claude` | Оба блока обязательны в минимальной форме; `enabled` указан явно |
| `providers.<name>.command.exec` | Абсолютный путь raw CLI, не shell, `env`, адаптер Bria или строка команды |
| `providers.<name>.command.argv` | Обязательный массив аргументов после argv[0], обычно `[]`; не shell-строка |

При `enabled:true` executable должен существовать и быть исполняемым.
Рядом с бинарником `bria` нужны `bria-codex-adapter` и `bria-claude-adapter`
для настроенных providers. Неизвестные/дублированные JSON-поля и trailing JSON
отклоняются. Модель/effort нельзя задавать через `providers.*.command.argv`:
валидатор направляет их в настройки самого провайдера.

Versioned форма имеет `version:1`, `role`, `computer`, `network`, `paths`,
`update`, `backup`, `parakeet`, `media_limits`, `providers`, а у coordinating
roles также Telegram/owner-поля. Необязательный `runtime` содержит полные
блоки `p4`, `discovery`, `backup`, `update`. Их точные поля и ограничения:
[schema и validation](../internal/config/versioned.go),
[платформы](PLATFORMS_AND_DEPLOYMENT.md). `backup.schedule` и
`backup.encryption` пока явно `null`. Принятие схемой роли `coordinator` или
`executor` не доказывает подключённый сетевой runtime; ограничения запуска
смотрите в [актуальном handoff](STATUS_AND_NEXT.md).

Проверка без Telegram loop:

```sh
bin/bria check-config --config /absolute/path/config.json
```

Она проверяет и локальные зависимости/secret references. `config.Decode` в
тестах проверяет только JSON/schema; это не заменяет `check-config`.
`check-telegram` уже обращается к сети, `run` меняет runtime state и запускает бота.

## Технический вывод в карточке

В меню настроек карточки задаётся `technical_output_lines`: 5, 10, 20 или 40.
По умолчанию и при отсутствии поля в старом settings-файле применяется 10.
Кнопка циклически переключает 10 → 20 → 40 → 5 → 10; значение сохраняется.
Это поле `<state_path>.settings.json`, не главного `config.json`.

Лимит считается после переноса по 100 Unicode-символов в строке, совместно
для аргументов и результата одного технического блока. Исходные переводы строк
также расходуют бюджет. JSON text/input_text blocks показываются читаемым текстом;
отметка сокращения расположена отдельно и не отнимает строку содержимого.
Настройка влияет на проекцию: новый сохраняемый вывод удерживает до 40 строк,
поэтому увеличение настройки раскрывает уже сохранённый текст. Старые ранее
обрезанные записи восстановить одной настройкой невозможно.

Новый код читает прежние settings и старый tool JSON. Обратная совместимость
старого бинарника с новым полем и компактным tool encoding не гарантируется:
для отката нужен согласованный совместимый набор бинарников и данных, не удаление
поля/истории или сброс пользовательского состояния. Установка отдельно согласуется.

## Совместимость журнала очереди

A25 revision 6 добавляет фазу input `terminal_failed`: подтверждённая ошибка
или остановка запроса, которая не блокирует следующие сообщения. Новый код читает
прежние журналы без преобразования старых `failed` в подтверждённый исход.

Бинарник без поддержки `terminal_failed` не сможет прочитать журнал после записи
такой фазы. Откат только бинарника на более старую версию небезопасен; нельзя
«чинить» совместимость удалением сообщений, сменой фаз или сбросом журнала.
Совместимый план отката и установка согласуются отдельно. Локальные тесты работают
на временных копиях; работающий журнал этой доработкой не мигрируется.

## Автоматическое сохранение финального ответа

Повтор записи финала включён как часть надёжного завершения, отдельного поля
конфига и ручной кнопки нет. Задержки: 1, 2, 4, 8, 16, 30 секунд, далее 30 секунд
до успеха, остановки Bria или смены точной привязки сессии. Повторяется только
сохранение полученного ответа; запрос модели не отправляется заново. Очередь ждёт.
Это не автоподтверждение команд и не зависит от `auto_approve_commands`.
Механизм не меняет формат состояния и не устраняет постоянные ошибки диска,
лимита истории или конфликта финала; их нужно исправлять без удаления истории.
Подробности отмены и восстановления - [жизненный цикл](SESSION_LIFECYCLE.md).

## Автоподтверждение команд

Настройки → CLI → «Автоподтверждение Codex». Хранимое поле:
`auto_approve_commands: true|false`. По умолчанию и при отсутствии поля в
старом settings-файле - `true`; явно сохранённое `false` переживает reopen.
Settings-файл имеет собственные `version`, `revision` и другие обязательные
поля: нельзя заменять весь документ одним новым полем.

ON разрешает Bria один раз выбрать `Yes, proceed` в распознанном активном
запросе исполнения команды, включая скрытую/неполную команду. OFF не отправляет
автоматическое «да» ни полному, ни неполному уже ожидающему запросу. Выключение
не откатывает команду, которая была подтверждена до него. Ручные кнопки остаются.
Переключение через UI сериализовано с отправкой; после успешного ответа OFF
прежний автоматический key не остаётся в очереди этого flow.

Файловые edits настроек применяются через штатную reload-процедуру, не через
обещание мгновенно перечитывать диск на каждый кадр. Для немедленного
выключения используйте кнопку. Мутация настроек меняет действующее значение
без перезапуска Codex. Перестройка launch argv относится к новым/возобновлённым
процессам; уже запущенный со старым bypass CLI не становится sandboxed от кнопки.

Это переключатель автоответа Bria, не запрет всех команд. Операции, на которые
Codex по собственной политике не просит разрешения, продолжаются и при OFF.
Он не управляет Claude, folder trust, hook trust, login или другими типами
диалогов. Образцы и live evidence: [LUNA_APPROVAL_CASES.md](LUNA_APPROVAL_CASES.md).

## Фактический запуск Bria

`runtimefactory` запускает соседний адаптер как
`bria-codex-adapter --native -- /absolute/path/codex <configured argv>`.
Адаптер нормализует argv, создаёт свой отдельный tmux server/socket и прямой
exec в рабочей папке сессии. Не отправляет собранную shell-строку в общий tmux.

```text
new:    codex <retained config args> --ask-for-approval on-request --sandbox workspace-write --no-alt-screen --cd <workdir>
resume: codex <retained config args> --ask-for-approval on-request --sandbox workspace-write --no-alt-screen --cd <workdir> resume <exact UUID>
```

Bria владеет `--cd`, new/resume identity, approval/sandbox policy. Сохранённые
`app-server`, `--stdio`, `--full-auto`, `--dangerously-bypass-approvals-and-sandbox`,
`-a/--ask-for-approval`, `-s/--sandbox` нормализуются, а не буквально исполняются.
Конфликтующие inline `-c/--config` ключи `approval_policy` и `sandbox_mode`
также удаляются. Остальные поддержанные настройки сохраняются; неизвестный
флаг или позиционный prompt отклоняется. Источник: [plan.go](../internal/nativecli/plan.go).

## Как запускает CCBot и что значит «как CCBot»

Отдельный агент прочитал текущий `/Users/a-s-nosko/pet_projects/ccbot` (`eedf111`); владелец
сверил ключевые места. `src/ccbot/config.py:87-108` задаёт:

```text
CODEX_COMMAND default: codex
CODEX_FLAGS default: --dangerously-bypass-approvals-and-sandbox --dangerously-bypass-hook-trust --enable hooks --no-alt-screen
```

`src/ccbot/tmux_window.py:164-221` создаёт окно общего tmux в `start_directory`,
склеивает command и flags в строку, добавляет `resume <thread-id>` при resume
и отправляет строку в pane через `send_keys(..., enter=True)`. Prefix env задаёт
`CCBOT_INTERFACE`, `CCBOT_AGENT_BACKEND`, `CCBOT_DIR` и необязательные runtime
references для бота/хоста/получателя. Эти ссылки не нужно копировать в Bria.
`CODEX_NAMING_MODEL` относится к названию сессии, не модели интерактивного чата.

Приоритет CCBot: уже заданный environment → локальный `.env` → `.env` в
`CCBOT_DIR` → defaults (`config.py:46-60`). `CODEX_FLAGS` можно переопределить.
Штатный startup watcher отвечает только на известные directory/startup prompts,
а default command approvals не парсит: они выключены самим bypass-флагом.

| Явно заданный случай в Bria | Фактический результат текущей версии |
| --- | --- |
| Raw executable Codex и `argv:[]` | Native tmux с `on-request/workspace-write`; автоответ зависит от settings |
| В argv скопирован только CCBot sandbox/approval bypass | Bria убирает bypass и применяет свою policy; это не буквальный режим CCBot |
| Скопирован весь default `CODEX_FLAGS`, включая `--dangerously-bypass-hook-trust` | Главный JSON может пройти schema, но native launch отклонит неподдержанный hook-bypass flag |
| Добавлен JSON `launch_mode:"ccbot"` | Такого поля сейчас нет, strict config его отклоняет |

Следовательно, **готового конфигурационного режима буквального запуска «как
CCBot» сейчас нет**. Документировать его как работающий было бы неверно.
Если он будет отдельно реализован, нужно явное решение о приоритете: с
CCBot bypass Codex не запрашивает command approvals, и OFF кнопка Bria не
способна вернуть отсутствующие запросы. Такой режим нельзя незаметно объявить
эквивалентным настройке `auto_approve_commands:true`. В этой задаче он не
добавлен, настройки/процессы работающих Bria и CCBot не изменялись.
