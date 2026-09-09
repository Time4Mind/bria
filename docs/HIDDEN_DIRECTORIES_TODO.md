# A31 - скрытые каталоги

## Договор

Источник - запрос Артёма 2026-09-09 и дополнение о переключателе настроек.
Результат: браузер каталогов по умолчанию скрывает дочерние директории, имя
которых начинается с точки; настройка «Скрытые каталоги» позволяет показать их
и сохраняется после перезапуска. Обычные имена с точкой внутри сохраняются.
Фильтрация до ограничения количества и пагинации, для локальных и удалённых
списков. Корни, переход к родителю, явно выбранный/default workdir, существующие
сессии и сами файлы не меняются. Период не влияет; evidence времени - UTC.
Разрешены реализация, тесты, документация и обычный автоматический выпуск
Time4Mind/bria main и gui/501/com.time4mind.bria.v2 по AGENTS standing authorization.
Не разрешены сообщения владельцу через Telegram, правки live config/state,
secrets, удаления и другие сервисы. Приёмка: RED/GREEN публичного поведения,
сохранение настройки, полная проверка, CI точного SHA, установленная сборка,
process/lock, свежие логи и сохранность пользовательских данных.

## Карта покрытия

- Main - integration owner, internal/sessioncreation (pure filter),
  internal/telegramcontroller (controller routing,
  публичные проверки), docs, release. Не меняет файлы других владельцев.
- Bernoulli - internal/settings, settingsport, settingscapability,
  settingscomposition, settingscodec: bool ShowHiddenDirectories=false, JSON
  show_hidden_directories, optional HiddenDirectoryPreferences с
  ToggleHiddenDirectories(context.Context), persistence/projection tests.
- Carver - internal/telegramsettings, telegramsettingsview, telegramsemantic,
  telegramui, callbacktoken, telegramcallbackview, telegrampipeline и
  telegramruntimecomposition: action settings_hidden_directories,
  CategoryCreation button/status, callback wiring и tests.
- Общий неизменный контракт - этот раздел. Границы файлов не пересекаются;
  UI и controller зависят от согласованного settingsport поля/метода.
  Критерий остановки агентов - профильные тесты и отчёт; без git/deploy.
  Внешние источники не нужны, full gate один раз перед выпуском изменённого кода.

## Обязательства

| ID | Результат / приёмка | Статус | Доказательство |
| --- | --- | --- | --- |
| A31.1 | Скрытие dot-directories до пагинации, оба режима | реализован | Local/remote listing RED→GREEN до page cap; hidden root доступен |
| A31.2 | Переключатель в настройках и persistence, old default false | реализован | Public controller toggle/close/reopen/browser GREEN; settings migration tests |
| A31.3 | Public consumer tests, independent review, full gate | выполнен | 14 профильных пакетов race PASS; full gate PASS; Bernoulli/Carver approve |
| A31.4 | Push, exact-SHA CI, restart и сохранность данных | открыт | ожидается |
| A31.5 | Диагностика voice/menu stale card | выполнен | Transport/state commit race, evidence ниже; исправление не включено |

Первый полный gate остановлен архитектурной проверкой: concrete persistence
test перенесён в settingscomposition; filter - публичная чистая функция
sessioncreation, чтобы не раздувать controller. Двойное чтение настроек убрано.
Поведенческий договор не изменён, тесты сохраняют прежнюю приёмку.

Ручной визуальный Telegram UI пока не проверен; тесты не будут выдаваться за него.

## Дополнение A31.5 - диагностика зависшего обновления

По новому запросу Артёма 2026-09-09 параллельно выяснить по логам, почему после
голосового карточка оставалась на распознавании, а после меню/возврата показала
семь страниц. Только диагностика текущего local service и соответствующего кода,
без реализации исправления, исходящих сообщений, изменения state/config.
Первичный период - 06:30 UTC 2026-09-09 до текущего момента, сузить до конкретного
voice/menu эпизода по безопасным correlation keys. Результат - цепочка событий,
сломанная граница, доказанная причина либо ограниченные гипотезы и недостающая
телеметрия; не путать непубликацию с отсутствием генерации. Отдельный read-only
агент - владелец этой зоны; main проверяет evidence и объединяет итог.
Приёмка - точные времена/классы событий и соответствующая ветка кода, без secrets
или сырого пользовательского текста. A31.5 выполнен; выпуск A31.1-4 остаётся в scope.

### A31.5 - результат диагностики

Диагностика выполнена, исправление не запрошено. Агент Archimedes и main
независимо прочитали bounded evidence: поздний receipt старого edit перезаписал
carrier новой голосовой карточки. Это не остановка распознавания или модели.
Период 2026-09-09 06:30–06:53 UTC, service PID18322/source dcb09d8.

| UTC | Проверенный результат |
| --- | --- |
| 06:43:26.729346 | Новая карточка отправлена, 1/1, card_ref c_4ba618… |
| 06:43:26.780382 | state.commit закрепил новую карточку |
| 06:43:26.892351 | Завершился более ранний edit старой карточки c_65ae6c… |
| 06:43:26.936287 | state.commit снова закрепил старую карточку, через 155.905 мс |
| 06:43:37.260882 | preprocessing_completed: processed |
| До 06:46:15.068350 | 28 успешных edits старой карточки, рост до 6/6 |
| 06:46:07.774749 / 06:46:10.388384 | Два клика новой карточки отвергнуты presentation_missing |
| 06:46:41.456739 | После menu_sessions новая привязка c_6a191a…, 7/7 |

Корреляция всех трёх карточек: session_ref c_a8649a…; вход
telegram-update:783531551. Main перечитал transport/state commit строки и
ветку commitCard: после HTTP receipt state.SetCard(want) без сравнения
ожидаемого прежнего carrier. CallbackRegistry.Bind тоже заменяет presentation,
retired сохраняется лишь при одинаковом carrier. Проверено отсутствие diff
этих веток относительно установленного dcb09d8; browser feature не участвовала.

Направление будущего исправления: устаревший receipt не должен менять актуальные
carrier/page/presentation. Нужен regression порядка old edit start → new send
commit → old edit finish, следующий refresh и кнопки должны остаться у новой
карточки. Сейчас это backlog без разрешения реализации. Live Telegram визуально
не осматривался; вывод опирается на подтверждённые transport receipts и state.
Четыре прежних unknown относятся к другим сессиям; не маскируют этот дефект.

## Bounded release manifest

Проверены origin https://github.com/Time4Mind/bria.git и ветка main;
существующий сервис gui/501/com.time4mind.bria.v2, current указывает на
20260909-speech-cleanup-shutdown, PID18322. Только следующие файлы:

- `docs/CONFIGURATION.md`
- `docs/STATUS_AND_NEXT.md`
- `internal/callbacktoken/codec.go`
- `internal/callbacktoken/command_lines_test.go`
- `internal/settings/codec.go`
- `internal/settings/settings.go`
- `internal/settingscapability/preferences.go`
- `internal/settingscodec/document.go`
- `internal/settingscomposition/preferences.go`
- `internal/settingsport/port.go`
- `internal/telegramcallbackview/technical_lines_test.go`
- `internal/telegramcallbackview/view.go`
- `internal/telegramcontroller/controller.go`
- `internal/telegramcontroller/session_creation_v2.go`
- `internal/telegrampipeline/command_lines_test.go`
- `internal/telegrampipeline/pipeline.go`
- `internal/telegramruntimecomposition/command_lines_test.go`
- `internal/telegramruntimecomposition/composition.go`
- `internal/telegramsemantic/action.go`
- `internal/telegramsettings/settings.go`
- `internal/telegramsettingsview/view.go`
- `internal/telegramui/keyboard.go`
- `docs/HIDDEN_DIRECTORIES_TODO.md`
- `internal/sessioncreation/directory_choices.go`
- `internal/settings/hidden_directories_test.go`
- `internal/settingscomposition/hidden_directories_controller_test.go`
- `internal/settingscomposition/hidden_directories_test.go`
- `internal/telegramcontroller/hidden_directories_test.go`
- `internal/telegramsemantic/hidden_directories_test.go`
- `internal/telegramsettings/hidden_directories_test.go`
- `internal/telegramsettingsview/hidden_directories_test.go`

После полного gate: commit manifest → normal push origin/main → оба CI точного
SHA → установка проверенного trio bria/bria-codex-adapter/bria-claude-adapter в
/Users/a-s-nosko/.local/opt/bria-v2/releases/20260909-hidden-directories →
атомарное переключение существующего current и restart только этого сервиса
через существующий plist. Без отдельного merge при direct main. Без force.
До restart перечитать idle состояние, после - version/hashes/PID/sole lock,
getMe, безопасные логи и hashes настроек/идентичностей/истории/journal.
Это terminal criterion выпуска; live сообщения и config/state edits исключены.

Полный make check-full PASS 2026-09-09 06:58 UTC: policy/secret scan,
format/architecture, plain/race/vet, packaging/операционные контракты и trio.
Cmd race67.671s, integration14.574s, controller9.163s; повторный public
browser/controller+settingscomposition race -count=5 PASS. Независимые review
settings/projection и filter/wiring approve. Лимиты архитектуры не повышались:
из view убраны два дублирующих empty-map guard, runtime использует существующий
type-preserving mapping. Production filter остаётся в sessioncreation.

Физическая сборка bria20260909-hidden-directories, check-config OK. SHA256:

- bria: 7a8ac0a0d99ffd045b6dd9967069cfad553a9d83b058e256fae07844bf50656a
- bria-codex-adapter: 792059a14d750aae8367d7b23d77434e0bc3cd604d01e7040e1f9800d4f41911
- bria-claude-adapter: 88d612486c8c3171489dd369673a2e93e05f6b84aa97092bc2403af850648263

Preflight06:56:46UTC: 6sessions, ready2/archived3/running1, history246,17inputs;
изменение истории произошло до выпуска в работающем сервисе. Перед restart
нужно заново проверить idle и взять актуальную базу сохранности, не сравнивать
ожидаемый пользовательский прогресс с устаревшим snapshot.

## A31.C1 - обязательный CI

7ce5ab0 запушен/readback origin/main. Platform34321594447 success;
Stage1 34321594611 failure: TestSecretSourceTombstoneSurvivesProviderAckPruneAndRestart,
flow_test.go:350, stale provider interaction callback. Локальный full gate до push
PASS. Выпуск не установлен. Разрешённая CI-fix iteration, не исправление A31.5.
Main диагностирует exact failing seam, independent agent проверяет гипотезу.
Начальная гипотеза: checkingSender.wait возвращает начало Deliver до сохранения
PhaseWaiting/carrier, тест преждевременно вызывает HandleCallback. Альтернативы:
таймаут, реальные ошибки persistence либо неверная correlation; проверить кодом
и контролируемым воспроизведением. Не ослаблять secret/source tombstone assertions.

Причина подтверждена: Deliver публикует sender.sent до receipt CAS PhaseWaiting;
сырой wait мог вернуть carrier ещё не зафиксированным. В FileStore на CI это
попало в окно 0.43с. Timeout=1minute и correlation fixture корректны.
Main владеет только internal/interactionflow/flow_test.go: каналами задержал
первый PhaseWaiting commit, первый Load возвращает прежнее состояние и освобождает
writer. Старый callback-тест детерминированно RED stale callback0.02s; теперь
9 callback-сценариев ждут durable PhaseWaiting с точным carrier, а raw delivery
wait сохранён для timeout/cancellation. Secret assertions и production не менялись.
Package race-count20 PASS2.366s; full gate повторяется. Этот test-only файл
добавлен к bounded manifest выпуска, новых runtime targets нет.

Повторный full make check-full PASS07:07UTC; физические trio hashes прежние.
Bernoulli независимо проверил CI-fix и подтвердил сохранность secret/tombstone
assertions, approve. C1 реализация проверена, требуется CI нового commit.
