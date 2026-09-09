# Подтверждения Codex Luna: живые кейсы для Bria

Дата проверки: 2026-09-08, Europe/Moscow. Codex CLI 0.153.4, macOS arm64,
модель `gpt-5.6-luna`, effort `medium`. Изолированная tmux-сессия
`bria-luna-probe-20260908`, pane `%43`, provider session
`01a07f71-f720-7971-952f-43b2e0d5c82c`. Модель, рабочая папка и политика
подтверждены `session_meta` / `turn_context` собственного rollout и экраном CLI.
Параметры теста: `-a on-request -s read-only --no-alt-screen`.

Публичная версия обезличена: номера рабочих тикетов, внутренние URL и пути,
содержимое и численные результаты источников здесь не публикуются. В fixtures
используются вымышленные идентификаторы; формы меню, переносы и отступы сохранены.
Исходные доказательства доступны владельцу только локально в игнорируемом
каталоге `.cache/private-approval-evidence`, который не включается в Git.

## Прочитанные источники

Все запросы выполняла Luna в этом терминале. Родитель наблюдал экран,
разбирал запрос и запускал одноразовое подтверждение. Продуктовый бот не
перезапускался; постоянные правила Codex не добавлялись.

| Сценарий | Физическое доказательство чтения |
| --- | --- |
| Локальная Bria | Прочитаны README.md, docs/NATIVE_CLI_PILOT.md, internal/nativecli/plan.go. В последнем Luna нашла фактические launch arguments. Подтверждение не потребовалось. |
| Рабочий Tracker-тикет | Live `get_issue`: прочитаны метаданные, затем непустое описание; точная цель и ответ сохранены локально. |
| Первая YT-таблица владельца | Проверены exists, schema и row_count; прочитаны ровно 3 строки по ограниченному набору неперсональных полей. |
| Вторая YT-таблица владельца | Проверены exists, schema и row_count; прочитаны ровно 3 строки; значения и пути сохранены только локально. |
| Внутренняя Wiki-страница | Получены метаданные, точная ревизия и непустое тело; содержание не публикуется. |

Wiki выбрана по ссылке из рабочего документа; точный URL хранится только локально.
Первый Wiki get_page вернул только метаданные; тело получено отдельным запросом
`fields=["content","last_revision_id"]`. Пустое поле в первом ответе не
считалось отсутствием содержания.

Первые сетевые попытки дали DNS/NETWORK без HTTP-ответа. После штатной
эскалации те же read-only операции выполнились. Это ограничение sandbox,
а не доказательство невалидного токена. Все выборки ограничены; секреты и
персональные идентификаторы строк в артефакты не включались.

## Корпус экранов

Шесть исходных экранов и отдельный переполненный экран находятся в
[nativeapproval/testdata](../internal/nativeapproval/testdata/).
Тест `TestCodexAllObservedApprovalFixtures` читает каждый файл корпуса.

| Файл | Запрос | Варианты |
| --- | --- | --- |
| codex-command-approval.txt | Метаданные Tracker | 3 |
| codex-command-approval-two-options.txt | Метаданные двух YT-таблиц | 2 |
| codex-command-approval-yt-rows.txt | Ограниченные строки двух YT-таблиц | 2 |
| codex-command-approval-wiki-metadata.txt | Wiki get_page по slug | 3 |
| codex-command-approval-wiki.txt | Тело и ревизия Wiki; многострочный Reason | 3 |
| codex-command-approval-tracker-description.txt | Описание Tracker; составная команда с `&&` | 3 |
| codex-command-approval-collapsed.txt | Повторный Tracker в окне 100×16: команда скрыта CLI | 3, неполный запрос |

### Переполнение окна - воспроизведено и обработано

На седьмом подтверждении тот же read-only Tracker get_issue проверен с
окном 100×16. CLI показал `[… 12 lines] ctrl + a view all` вместо environment,
reason и preview. `capture-pane -S -70` также не вернул скрытый блок: его
не было в отрисованном терминале. Первоначальный parser возвращал
`recognized=false`. После уточнения Артёма неполный command preview также
разрешён: текущий parser возвращает `recognized=true`, `incomplete=true`.

Штатный Ctrl+A открыл отдельный EXEC viewer с прокруткой и `q to quit`.
Этот viewer не является approval-меню: в нём нельзя нажимать Enter как «да».
Закрытие viewer и увеличение окна вернули исходный диалог.

В [CaptureComplete](../internal/nativeapproval/capture.go) реализовано ограниченное
расширение принадлежащего сессии окна: 80, 160, максимум 256 строк, ширина
сохраняется. После каждого изменения выполняется новая capture с ожиданием
перерисовки. Из обрезанных кусков команда не реконструируется. Это opt-in
диагностика `--expand`. Штатный observer не расширяет окно: промежуточный
эксперимент с авторасширением воспроизвёл stale hash между snapshot и key.
Прямое принятие recognized incomplete устраняет необходимость менять canvas.

Живой прогон обновлённой сборки: 100×16 → 100×80 → полный request →
fingerprint `05efb25e2c2296133ccb764b64e151caf9705009d3d37e2692de809a2033a5c7` →
одноразовый Enter → новая строка Codex approval receipt → Tracker
ответ с ожидаемым ключом тикета и его статусом (исходный ответ сохранён локально).
Повтор этого fingerprint отклонён durable claim без новой клавиши.
Расширение не уменьшает окно после чтения; по завершении диагностического
прогона размер можно восстановить отдельно.

Восьмое живое подтверждение проверено **без расширения** окна 100×16 через
реальные `settings.FileStore` → `settingscomposition.Preferences` →
`nativeapprovalflow.Flow` → tmux executor. OFF: три наблюдения, 0 NativeControl,
исходный диалог остался. ON: один Enter, явный Codex receipt, повтор подавлен.
Fingerprint: `9b7240bbc6c2cb5267c2f5f55f7febd11b873905d720f901d8804283ae3b6b1e`.
Следом Luna получила ответ с ожидаемым ключом тикета и его статусом.
Тест: `TestLivePendingIncompleteApprovalHonorsDurableOffThenOn` в
[live_settings_test.go](../cmd/bria-approval-probe/live_settings_test.go).

Даже после лимита 256 строк recognized incomplete остаётся actionable при ON.
Не требуется наличие полного Environment/Reason/Preview, если CLI показал
известный collapse marker. Нужны признаки именно текущего approval-меню,
известные варианты и выбранное одноразовое «да». Произвольный экран, потерявший
все эти признаки, и конкурирующий ручной ввод не покрыты этим контрактом.

Это один наблюдавшийся тип - подтверждение исполнения команды - с двумя
формами меню. Шесть источников запросов не означают шесть типов протокола.
Не наблюдались approvals на изменение файлов, managed network host access,
MCP elicitation, tool-call approvals, вход в аккаунт и hook trust. Их нельзя
автоматически считать эквивалентными этим образцам.

## Контракт парсинга и ответа

Общий [ParseScreen](../internal/nativecli/ready.go) возвращает поле
`CommandApproval`. Разбор и одноразовый ответ реализованы в
[nativeapproval](../internal/nativeapproval/codex_approval.go).

1. Выделяется последний активный диалог с точным заголовком
   `Would you like to run the following command?`. Внизу должен быть точный
   `Press enter to confirm or esc to cancel`, без нового composer под ним.
2. Извлекаются доступные `environment`, многострочный `reason`, `preview`,
   `option_count`, `fingerprint`, `incomplete`. Поддерживается наблюдавшееся
   `local` и скрытые поля при известном CLI collapse marker.
3. Должен быть выбран именно `› 1. Yes, proceed (y)`. Затем разрешены только
   наблюдавшиеся формы: отказ №2 либо persistent option №2 и отказ №3.
   Обрезанная команда допустима; неизвестный тип меню, изменённый выбранный
   вариант или старый экран не являются actionable event.
4. SHA256 охватывает весь блок, включая отступы команды и варианты ответа.
   Это идентичность preview, не независимая авторизация действия. Причина
   «read-only» сама по себе не даёт права выполнять команду.
5. По разрешённой владельцем политике и только при действующем ON вызывается
   `AcceptCodexCommandOnce` с ожидаемым fingerprint и терминалом той же сессии.
   Перед Enter экран читается дважды. В diagnostic режиме отдельно задаются
   exact identity/fingerprint; штатный flow передаёт hash/provider/generation.
   Одно нажатие выбирает одноразовое «да»;
   persistent rule не выбирается.
6. Проверяется новая видимая строка `✔ You approved codex to run ...`
   и отсутствие интерактивного меню. Пустая перерисовка - не receipt.
   После этого отдельно проверяется tool output: receipt не доказывает успех API.

Preview может переносить строку посреди пути, Python-строки или выражения.
Его запрещено склеивать эвристикой и повторно исполнять как shell-код.
Автоматика нажимает клавишу в уже существующем запросе Codex.

## Локальный исполнитель

[bria-approval-probe](../cmd/bria-approval-probe/main.go) использует тот же
пакет, что и native-парсер Bria. Сначала inspect по точному pane ID; затем
accept-once по проверенным identity и fingerprint. Поиск «самой новой»
сессии не используется. Все вызовы должны пользоваться одним state-dir.

```sh
bin/bria-approval-probe --target %43
# Для CLI-collapsed диалога, без отправки решения:
bin/bria-approval-probe --target %43 --expand
# После проверки точного preview и tuple из inspect:
bin/bria-approval-probe --target %43 --identity 'SESSION:WINDOW:PANE:PID:0' --accept-once SHA256
```

Перед записью повторно проверяется tuple session/window/pane/PID/alive.
Одноразовый durable claim через O_EXCL блокирует параллельные и повторные
запуски. Claim сохраняется и после неопределённого исхода: автоматически
удалять его и повторять Enter нельзя. В обычном runtime терминалом должен
владеть один actor; конкурирующий ручной ввод не является поддержанной
границей атомарного подтверждения.

## Настройки и штатный путь

Настройки → CLI → «Автоподтверждение Codex», поле `auto_approve_commands`
в `<state_path>.settings.json`. Default/legacy missing ON, явно сохранённый OFF
не заменяется default при reopen. Схема, launch policy и CCBot compatibility:
[CONFIGURATION.md](CONFIGURATION.md).

Штатный путь: native observation → generation-bound snapshot →
[nativeapprovalflow](../internal/nativeapprovalflow/flow.go) → live settings gate →
correlated `approve_once` → adapter повторно проверяет экран → один Enter → receipt.
Оба вида диалога используют один gate. Отключение и dispatch сериализованы;
отменённый toggle не ждёт зависший provider. Dispatch ограничен context deadline.
Привязка к provider/generation проверяется до записи и не выбирает «новую» сессию.

Decision claim внутри flow связывает provider/generation с fingerprint конкретного
распознанного диалога. После подтверждённого одноразового решения следующий
отличающийся command approval принимается сразу, даже если параллельные tool calls
заменили один диалог другим без промежуточного неинтерактивного кадра. Тот же
fingerprint не повторяется. Новый receipt вместе с уже отличающимся активным
fingerprint подтверждает предыдущий Enter без промежуточного пустого кадра.
Неопределённый исход сохраняет более строгий барьер
на всю generation: collapsed → full, непустой неинтерактивный кадр и другой
диалог не получают второй Enter до смены generation. Внутрипроцессный claim не
является durable runtime-журналом через перезапуск контроллера; durable O_EXCL
claim предусмотрен отдельно у diagnostic helper.

## Ограничения и проверка

Screen capture не содержит надёжного request ID и не связывает receipt
криптографически с командой. Между capture и send-keys остаётся гонка при
вмешательстве другого владельца терминала. Это не классификатор безопасности
команд и не автонажатие произвольного «да» во всех меню. В тесте разрешены только
перечисленные read-only источники; в продукте поведение задаётся владельцем
через общий ON/OFF для распознанных command approvals.

Для структурированной интеграции Codex документирует
`item/commandExecution/requestApproval` с threadId, turnId, itemId и JSON-RPC id;
ответ `accept`, затем `serverRequest/resolved` и `item/completed`.
Это ориентир для app-server, не наблюдавшийся в данном TUI-тесте wire capture:
[официальная документация](https://learn.chatgpt.com/docs/app-server#approvals).

Регрессии покрывают оба layouts, весь корпус, старый экран, отсутствующие поля,
обрезку, иной выбор/окружение, изменившийся fingerprint, сохранность Python
отступов, перерисовку, повтор, конкурирующие claims и смену PID. Интеграционный
тест проверяет событие через общий ParseScreen Bria. TDD: реальный экран сначала
не распознавался; отдельные тесты воспроизвели whitespace collision и ошибочное
принятие пустого кадра, затем прошли после исправлений.

Полный выпуск не выполнялся. Штатный observer подключён в исходниках;
settings/callback/observer проверены через публичные границы, `approve_once`
прошёл через настоящий tmux и native adapter на локальной fixture-программе.
Тестовые файлы и исполняемые probe-артефакты не означают deployment.
Live Telegram acceptance, restart рабочего бота и смена его конфига не выполнялись.
