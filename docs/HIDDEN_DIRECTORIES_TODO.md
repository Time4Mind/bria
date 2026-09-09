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

## A32 - автоматическая связь с живым терминалом (текущий договор)

Явное дополнение Артёма 2026-09-09: обычный запрос не должен терять сессию,
пока терминальная сессия жива. После временного сбоя наблюдения либо перезапуска
Bria связь с тем же терминалом восстанавливается автоматически, без ручных
кнопок «восстановить» и бессодержательных сообщений. Закрытый терминал отличать
от временно недоступного наблюдения. Не считать подавление ошибок исправлением.
Main объявил включение исправления A31.5 carrier race в общий выпуск.

Новый единый scope сохраняет все A31 обязательства и добавляет A32 ниже.
Разрешены код/тесты/документация, безопасное восстановление существующего binding
и общий normal push/exact CI/deploy существующего сервиса по standing authorization.
Не разрешены повторная отправка accepted/unknown запроса, выдуманное завершение,
удаление истории, автоматическое создание замены закрытому терминалу, обход
approval prompts, правки secrets и прямые ручные записи live config/state.
Временные ошибки сохраняются в диагностических логах; UI не должен требовать
ручного восстановления, если точная живая сессия может быть найдена автоматически.
Ожидание ответа на прежний async-вопрос о моменте restart не блокирует разработку;
новое требование явно включает восстановление связи после restart Bria.

Приёмка: public RED/GREEN oversized transcript event/read continuation, точное
reattach к живому terminal при временном сбое и restart с принятым input без
повторного submit, закрытый терминал не маскируется как живой; old-edit/new-send
race сохраняет новую карточку/кнопки/следующий refresh. Full/race/architecture/CI,
установленные artifacts/process/lock, actual recovery и сохранность истории.
Если внешний приёмочный сценарий потребует новых сообщений или остановки самого
CLI, сначала согласовать именно это действие. Логи читать bounded, время UTC.

### A32 coverage и владение

- Main - integration owner, docs и выпуск; controller-facing recovery UX,
  internal/telegramsessionview и internal/telegramui, затем orchestration
  после findings Archimedes. Не редактирует чужие пакеты.
- Bernoulli - writer internal/nativetranscript и его tests: причина oversized
  record, bounded memory/continuation без потери terminal/final identity.
- Carver - writer internal/telegramflow и internal/telegrampipeline:
  stale delivery commit/carrier/presentation race, deterministic public regression.
- Archimedes - read-only owner mapping/recovery diagnosis: nativeterminal,
  nativeadapter, sessionruntime, recoveryruntime и composition; определить exact
  живой terminal и причину перехода awaiting_recovery, минимальные точки фикса
  и тестовые seams для main. Если нужен отдельный writer scope, main уточнит карту.
- Зоны независимы; общие API менять через main, каждый файл имеет одного writer.
  Stop - доказанный RED/GREEN и review либо конкретная непроверенная граница.
  Не запускать полный gate каждым агентом; main запускает после объединения.

| ID | Результат / приёмка | Статус |
| --- | --- | --- |
| A32.1 | Oversized/temporary observation failure не роняет живую сессию | exact compacted root исправлен; общий parser/observation остаётся открыт |
| A32.2 | Exact automatic reattach/recovery после разрыва/restart, без duplicate submit | полный объём согласован ответом «Да», реализация |
| A32.3 | Carrier race и кнопки новой карточки | локальный код/RED-GREEN/review/full gate PASS; legacy fence отдельно, не выпущено |
| A32.4 | Проверенный общий выпуск A31+A32 и live postflight | открыт |

Состояние A31 до расширения: 4c918a39d534828b47e829dc32893667fed2dcf1
запушен/readback; Platform34322308028 и Stage134322308005 success. A31.C1 закрыт.
Бинарники hidden-directories локально, install mode не запускался, service18322
never exited. В07:08:52.432820 timing.terminal=error с категорией
native_transcript_record_too_large,07:08:52.442735 awaiting_recovery.
Вход783531551 completed;783531558 accepted, native receipt unknown, pendingoutputs0.
Это не завершение принятого запроса; его не отменяли и не переотправляли.

### A32 текущие проверки

Main UI RED→GREEN: TestAutomaticRecoveryCardDoesNotDemandOwnerIntervention
воспроизводит лишние «Требуется восстановление / ввод отключён» при доступном
auto recoverer. RecoveryNotice не выводит ручную инструкцию при available/busy,
статус Recovery остаётся истинным, история доступна, input safety guard сохранён.
TestRecoveryKeyboardPreservesHistoryWithoutManualRepair RED→GREEN:
бесполезная кнопка «Восстановить» убрана для Recovery-карточки; close, история,
пагинация и смена сессии сохранены, обычный Resume архива не меняется.
Профильный race controller0.627s/ui0.313s PASS. Это только UX-часть;
автоматический lifecycle A32.1/A32.2 ещё не принят и не заменяется этой проверкой.

### A32.2 архитектурный факт перед изменением

Main и Archimedes подтвердили по текущему коду: sessionruntime.Shutdown вызывает
Abort для всех adapters; nativeadapter.Run defer вызывает Terminal.Close;
nativeterminal.Close уничтожает pane tree и частный tmux server. Starter.Start
делает orphan cleanup и новый launch. В Supervisor.Watch/RecoverPersisted
accepted unknown блокирует восстановление до запуска, LiveSupervisable исключает
awaiting_recovery из повторного sweep. Поддержки exact live terminal attach нет.

На07:17:36UTC точный проблемный CLI не найден в дереве Bria; найден terminal
другой сессии. Это не абсолютное доказательство отсутствия отдельного orphan.
Большой transcript record - отдельный подтверждённый источник fatal наблюдения.
Одной правкой parser или скрытием notice требование A32.2 не выполняется.
До реализации нового terminal lifecycle main оценивает объём с учётом указания
Артёма отдельно согласовывать доработки масштаба тысяч строк. Пока разрешённые
независимые A32.1/A32.3 и тесты UX продолжаются; lifecycle не менялся.

Оценка Archimedes: full exact terminal persistence/attach 900–1500 production
и1300–2100 test LOC (всего2200–3600), предварительная, не измеренный diff.
2026-09-09 main направил владельцу async-вопрос о полном объёме по его прежнему
ограничению; до ответа lifecycle не реализуется. Независимые fixes продолжаются.
Первый deploy также требует учесть, что старый процесс сам закрывает терминалы:
новый бинарник не меняет Shutdown уже запущенной старой версии.

Ownership уточнён: Bernoulli также writer internal/nativejsonline и tests,
чтобы расширить существующий framer вместо создания второго parser.
Carver прежний writer carrier flow; main/Archimedes read-only проверяют самый
компактный корректный commit-lock/CAS design перед расширением в state/producer.
Archimedes lifecycle estimate завершил и теперь независимый reviewer carrier
lock-order, receipt idempotence и необходимости persistent revision; файлов не меняет.

Main повторно проверил full scoped UI race: controller7.621s/ui0.401s/
cards0.684s PASS. Прямое bounded перечитывание service.jsonl:31 и detailed.jsonl:1104
подтверждает native_transcript_record_too_large в07:08:52.432820/07:08:52.439084UTC;
это две записи одного сбоя, не два независимых возникновения.
Snapshot07:30:39UTC:6sessions ready2/archived3/awaiting_recovery1,18inputs,
history300. Hashes идентичностей/cards/journal/settings/config/plist совпадают
с07:11:38UTC; сервис не перезапускали и ручной live state write не выполняли.

Уточнение exact RCA: bounded metadata native transcript показывает последнюю
большую запись type=compacted в07:08:52.377UTC,1360108bytes, включая
replacement_history7/guardian_history130 элементов. После неё ещё несколько
служебных записей до07:08:52.384UTC, финал текущего turn не найден среди последних
18 записей. Это не доказательство финала/завершения. Bernoulli получил exact shape
и обязан добавить compaction regression; oversized tool test не заменяет её.

Archimedes review carrier: state→registry lock-order допустим; ABA достижим
через выбор сессии на чужом carrier. Нужен небольшой durable fence, не generic
transaction subsystem. Exact registry replay должен сохранять claims.
Ownership main расширяет: Archimedes writer internal/telegramstate и
internal/telegrampromptcomposition/internal/telegramcompletioncomposition,
согласовав узкий API с Carver; Carver flow+pipeline. Storage.SetCardCarrier уже
вызывает State.SetCard, отдельный writer storage не требуется. Lifecycle отдельно
остаётся на согласовании объёма, это назначение касается только carrier race.

Main ownership дополнение: internal/storage/session_store_test.go, только
ожидание CarrierRevision=1 после первой физически сохранённой карточки в тесте
legacy migration. Consumer pass выявил точный additive metadata diff; остальные
поля/утверждения не ослаблялись. Narrow race migration PASS0.362s; controller
7.374s/runtimecomposition0.333s PASS. Interim architecture gate PASS; это не
финальная проверка ещё меняющейся версии. Public read-only exact transcript
probe RED: offset5153232, observed1360107, limit1048576, перед ошибкой один batch.

После узкого allowlisted top-level compacted изменения main повторил тот же
read-only probe на физическом журнале:16 bounded Poll, error=null,
commentary12/complete1/final1/model1/tool138/user2. Это GREEN чтения исходного
журнала без падения и без повторного input; не восстановление live CLI и не
завершение нового unknown input. Production service по-прежнему старый.

Bernoulli exact-root diff заморожен: net+2 production, новый compacted_record_test.
Poll/Scan/physical offsets/exact identity/adjacent finals/partial/cancel/retry,
nested discriminator и duplicate-key rejection RED→GREEN. Профильные full
plain/race/vet PASS, race14.700s/2.524s. Main видел физический GREEN отдельно.
Это исправление конкретной compaction-записи, не общий arbitrary oversized parser:
лимит структуры и fail-closed значимых/неизвестных больших записей сохраняются.
Exploratory oversized tool test writer убрал из текущего diff; tool projection
не реализована и не объявляется готовой. A32.1 полностью пока не закрывается.
Bernoulli теперь read-only independent review carrier, его parser files frozen.

Main cmd/bria targeted Recovery/CardRefresh/Navigation race PASS5.497s.
Interim architecture выявил completioncomposition277>275, owner Archimedes
исправляет без повышения лимита. Другие лимиты проходят; весь diff ещё меняется.

Archimedes carrier state/producers заморожены: CarrierRevision сохраняется,
растёт только при смене carrier (0→1→2→3 при A→B→A), history/page не сбрасывают
счётчик; LastPresentationOperation ограничен/валидируется. Producers routine
card и native-screen refresh передают копию ExpectedCarrierRevision *uint64.
Legacy отсутствие поля=0; overflow отклоняется без мутации. Reopen и producer
assertions/race/vet PASS. Размер этой части примерно180–220 строк вместе сtests.
Completion CardStore переиспользует identical carddeliveryguard.Store alias,
убрано дублирование интерфейса:275/275. Main current architecture PASS послеfix.
Archimedes теперь read-only review parser; Bernoulli read-only review carrier.

Main integrated race11 затронутых пакетов PASS: transcript15.118s/framer2.629s,
flow17.831s/pipeline1.558s/state1.129s/prompt2.701s/completion0.856s/storage35.815s/
controller9.265s/runtime2.368s/ui0.637s. Arch independent parser review approve,
свежий race14.258s/2.570s. Но Bern carrier review нашёл HIGH: delayed callback
receipt не получает revision fence и может вернуть старый carrier после нового
send, в том числе при replay после отказа UI commit. До исправления не approve.
Уточнение ownership для ускорения: Arch writer только нового
internal/telegramflow/callback_carrier_replay_test.go (public RED/GREEN HIGH);
Carver исключает этот файл, владеет production flow/pipeline и прежними tests.
Bern read-only minimum callback snapshot design; main integrator. Это прежний
scope carrier race, не новая функциональность и не разрешение full lifecycle.

Release compatibility boundary main verified: storage/session_store.go:999
использует DisallowUnknownFields, поэтому установленный старый dcb09d8 не сможет
напрямую прочитать session state после записи новых carrier_revision/
last_presentation_operation. Простое переключение current назад не является
проверенным rollback. До выпуска подготовить и проверить совместимый путь
возврата без потери новых inputs/history; не удалять поля/не заменять live state
старым snapshot вручную. Это открытая проверка A32.4, пока install не выполнялся.

Arch callback HIGH public RED заморожен: новый154-строчный
callback_carrier_replay_test.go, реальный FileUI/registry/operations reopen.
Plain иrace: replay старого receipt возвращает100/rev2/page3-of-3 в
99/rev3/page1-of-2, кнопки100 stale; history/pending иno-resend уже проходят.
Carver получил RED и исправляет callback snapshot fencing. Этот тест нельзя
ослаблять до прохождения; он блокирует объявление A32.3 готовым.

Callback HIGH GREEN: Arch plain0.427s/race0.461s, замороженный test hash/expected
не менялись. Snapshot до effect fence/Executor, target revision либо явный
ExpectedCarrierAbsent сохраняется до CallbackPrepared. Navigation проверяет
target, не требует равенства source carrier. Bern code-review HIGH закрыт,
новых доказанных блокеров нет. Legacy Prepared без fence не получает задним
числом доказанную revision - отдельная миграционная граница. Normal final
automatic resend исключает existing messagejournal phase gating, не этот guard.

Interim flow2525>2500. Main разрешает Carver дополнительно только новые
internal/telegramui/clone.go иclone_test.go для переноса value-copy projection/
keyboard в владельца UI types, если существующего reuse недостаточно. Другие
UI files остаются main. Не повышать cap и не сокращать обязательные проверки.

Carver freeze: carrier edit/send/ABA, callback snapshot перед эффектом,
same/different/absent target navigation, snapshot read failure оставляетClaimed,
concurrent history append не ломает известный receipt. Exact registry replay
сохраняет claims, UI+registry projection сериализуются state lock. Final
plain/race/vet flow/pipeline/ui PASS; race14.176/2.371/1.798s.
Clone projection/keyboard перенесён в UI27production lines, existing clone
keyboard helper переиспользован. Flow2492/2500, pipeline1699/1700, cap не повышен.
Main full make check-full candidate20260909-recovery-hardening-candidate идёт;
policy/links/secrets/format/architecture/scripts PASS на замороженном diff.
Это подготовительная проверка, не выпуск и не закрытие A32.2/A32.4.

Первый full gate: plain все затронутые carrier/parser consumers PASS, но6
nativeadapter real-terminal tests падают при запуске tmux за0.03–0.04s
(`native terminal server exited during startup`), terminal/runtime код не менялся.
Проверка гипотезы sandbox: main повторяет тот же make check-full вне sandbox
через require_escalated, не меняя код/фикстуры и не трогая рабочий service.
До результата причина ограничения окружения предполагается, не доказана.

Финальный full make check-full PASS2026-09-09~07:59UTC внеsandbox:
policy/links/secrets/format/architecture/plain/race/vet/packaging/build/trio.
Без изменения terminal-кода nativeadapter plain9.231s/race12.879s PASS;
разница изолированного окружения подтверждена повтором, не чинилась кодом.
Race cmd73.766s/integration16.620s/flow24.861s/transcript19.217s/storage39.608s.
Физическая сборка20260909-recovery-hardening-candidate, --version/check-config OK:

- bria d0a0afc0f9ed657e93e8e7b2f7c523802591951381172b48e071af191b6c377e
- codex-adapter6081d48c591a634fd2d27a20a3522cb7285d957d7e40e22a881e966d8572de68
- claude-adapterbb2408d14d862218baf6e58908e46c03815be1f4335b767bd9bc9ce4b5aa32f9

Не установлен, A32 diff не committed/pushed; готовность кандидата не заменяет
полный запрос. Snapshot07:57:33UTC по-прежнему6sessions/18inputs/history300,
ready2/archived3/awaiting_recovery1, canonical hashes идентичностей/cards/journal/
settings/config/plist прежние. Текущий unknown input не повторяли/не отменяли.
Все writers остановились на проверенном diff. Следующее необходимое решение:
ответ владельца на full lifecycle2200–3600LOC сtests (async вопрос отправлен,
ответа нет); это development scope gate по его прежнему ограничению масштаба,
не повторное согласование обычного push/deploy. При ОК продолжить A32.2, общий
oversized/observation scope A32.1 и migration/rollback A32.4; затем снова full gate,
автоматический push/exact CI/установка существующего сервиса и live acceptance.
До этого UI removal - лишь подготовка: controller input guard и настоящая
awaiting_recovery причина не заменяются скрытием notice, live восстановления нет.

## A32.2 full - разрешение и текущая карта

Ответ Артёма «Да» после вопроса о2200–3600LOC сtests явно разрешает полный
вариант. Договор A31+A32 выше неизменен; scope gate снят. Старые записи об ожидании
согласования исторические. Main доводит весь запрос, затем normal automatic
commit/push/exact CI/deploy существующего macOS service. Новые сообщения,
secrets, принудительное завершение чужого CLI и ручные live state edits исключены.

Приёмка full: exact accepted message→отсоединение observer/restart Bria→те же
CLI PID/pane/provider session→новый observer, без нового submit→прогресс и финал
ровно один раз. Ready без unresolved input, Running при подтверждённом живом
accepted turn; временный сбой не закрывает CLI. Закрытый CLI не выдаётся заживой.
Нужны bounded logs, отдельные ошибки identity mismatch и временной недоступности,
first-deploy/strict-state rollback, сквозные проверенные artifacts и live postflight.

Карта текущей реализации (старые parser/carrier файлы frozen):

- Main: integration, nativeadapter, sessionruntime и runtimeprotocol;
  соединяет exact terminal attach с read-only ObserveAccepted protocol,
  controller integration, docs/architecture registration и выпуск.
- Archimedes: writer internal/nativeterminal +tests, новый bounded manifest
  package при необходимости через main. Persistent isolated tmux, exact attach,
  detach-vs-close, PID reuse/identity proof, real-process survival tests.
- Carver: writer sessionsupervisor/sessionrecoverycontrol/supervisioncomposition
  и необходимые узкие app/domain recovery ports после согласования с main:
  automatic attach before unknown reconciliation, сохранение Running,
  bounded retry и stale generation fencing. Не меняет runtime/nativeadapter.
- Bernoulli: пока read-only owner accepted-turn continuation seam:
  acceptedrecovery/durableflow/input replay integration. Предлагает minimal
  ObserveAccepted→существующие history/final/journal consumer APIs без submit;
  затем main назначает непересекающийся writer scope. Не меняет agent-owned files.
- Все получают этот же договор. Новые public API согласуются письменно между
  владельцами. Main сейчас реализует protocol/runtime boundary; исполнители
  не делают git/deploy и запускают только профильные проверки. Stop каждой зоны:
  public RED/GREEN, неизменный payload/correlation, явные открытые границы.

Main progress2026-09-09~08:19UTC:

- Runtimeprotocol RED→GREEN: observe_accepted несёт толькоrequest_id/message_id,
  любыеinput/command/model/attachment fields запрещены; detach отделён отclose;
  stableevent_id проходитdecode/encode. Полныйprotocol plain/race PASS.
- Sessionruntime RED→GREEN: Attach поднимаетobserver сBRIA_ATTACH_ONLY, повышает
  толькоgeneration; ObserveAccepted использует общийкоррелированныйconsumer без
  submit, Shutdown native→detach. Runtime/factory nativeopt-in тест PASS;
  полныйruntime race10.934s. Последующиеcomposition/terminal изменения этим не покрыты.
- Nativeadapter observed-turn RED→GREEN: exactreceipt+turnID, свежийreader,
  чужойfinal/complete игнорируются, 4frames accepted/event/final/completed,
  stableeventID, durablecompleted. nilterminal вfixture доказывает отсутствиеInput.
- Найден дополнительный реальный long-turnlimit: runtime прерывал поток после
 256events даже сOnEvent. RED2events→failure, GREEN700events→final;
  callback получает всеevents, retained resultprefix bounded, no-callbacklimit прежний.
- Coverage уточнена: Bernoulli writer durablecomposition continuation+controller
  existingworkerreuse/historydedup; main singlemachinecomposition hookup.
  Archimedes послеterminal владеет adapter.go startup/Main; mainobserve.go завершён.
- Размерныеграницы не повышаются: main выделил immutablecommand validation
  вruntimecommand(150budget), terminalbinding(600budget) зарегистрирован для
  exactidentity/lease. Native terminal и supervisor владельцы выделяютответственности.
- Live snapshot08:18:59UTC прежний6sessions/18inputs/history300, identities/cards/
  journal/settings/config/plist hashes идентичны07:57. Bria18322 running.
  Read-only ps внеsandbox: adapter18334 иtmux18405 разделяютPGID18334;
  pane18409→codex18419 живы. Поэтому firstdeploy oldshutdown сохраняет реальный
  риск закрытия старогоCLI; миграция не выполнена, corestate не правили.

Main progress08:29UTC:

- Full runtimeprotocol/sessionruntime/runtimecommand/native recovery factory/
  runtimefactory/singlemachinecomposition race PASS14.774/0.629/1.247/2.739/
 0.859s. Cancellation RED/GREEN: послеaccepted отменаobserver не пишетinterrupt;
  stalegeneration отклоняется доwirewrite.
- Установлена отдельнаязакрывающаяграница TypeClosed: persistent adapter должен
  подтвердить exactprovider_session_id послеphysicalClose. EOF/detach/reaping
  не доказываютзакрытиеCLI. REDdetachedtombstone→falseclose, GREENошибка;
  closedack→успешныйидемпотентныйAbort. Forcedfactoryfixture по-прежнему проверяет
  смертьвсегодерева, но теперь сохраняетmissingphysicalproof какошибку.
- Startuplifecycle подключён main: сыроеAcceptedObserver передаётся отдельно от
  P4/observabilitysubmitter; exactattach/recovery запускается только послеbind
  history/final/card consumers. Existingruntimecapabilities сохранены wrapper
  nativerecoverycomposition.Runtime; прежниеgenericstdio пути остаются.
- Carver получилзакрытие/разархивацию: отключить legacyNotTracked→Archive для
  nativeattach, Attach+Detachrollback дляarchivedalive, продолжитьaccepted
  close-after-work. Bern получилпроверкуacceptedphaseбезUnknown, multi-input
  continuationqueue, controllerShutdownDetach иустранениеmanualrestoreguard.
- Никакогоgit/deploy/liveinputне было; mainждётreadyownerграницы иполныйgate.

Main progress08:58UTC:

- Runtime event identity теперь сохраняется атомарно вместе с текстом и kind
  через cardeventhistory/storage. Восемь параллельных store instances и reopen
  дают одну запись; конфликт содержания для того же ID отклоняется. Full storage/
  cardhistory/telegramhistory/acceptedrecovery/recoverycomposition race PASS.
- Native TurnID проходит recovery receipts до continuation. Bernoulli владеет
  группировкой accepted root+steer, общей history/final pipeline и controller
  extraction в turncontinuation. Проверка повторного финала для уже completed
  группы и retry после ошибки journal остаётся открытой.
- Explicit unarchive использует Start exact resume: managed alive сначала Attach;
  новый CLI с тем же native ID разрешён только при точном ErrNotManaged. Фоновый
  recovery всегда Attach-only, отсутствующий manifest не запускает CLI. Archimedes
  владеет adapter startup/attachments; Carver app/recovery зона frozen и review.
- Carver нашёл блокировку повторного Attach после rollback: durable g7 оставался,
  а tombstone уже g8. RED второй попытки -> GREEN g8/g9/g10 без повторного номера,
  только для exact исходного prior binding; unrelated g6 отклоняется. Полный runtime
  race16.917s PASS. Native Detach wire branch направлена Archimedes на исправление.
- Добавлен offline read-only check-state и rollback guard до изменения pointers:
  старый binary без точного receipt отклоняется. RED несовместимый rollback реально
  переключал version -> GREEN сохраняет current/previous/config. cmd focused,
  scripts и packaging PASS. Это безопасный отказ от несовместимого отката, а не
  доказательство возможности возврата на старый dcb09d8 после новых state writes.
- Snapshot08:56:51UTC совпадает с08:18:59:6sessions/18inputs/history300, hashes
  identities/cards/journal/config/plist/settings неизменны. Live state не правили,
  input не повторяли, git/deploy не выполняли. Первый переход со старой версии
  с общей PGID ещё требует проверенного безопасного handoff.

Scale gate09:00UTC:

- Фактический текущий Go diff (включая предыдущий A32 partial): production
  705added/998removed +2329new lines =2036 net; tests203added/34removed
  +4006new lines =4175 net. Итого6211 net, большая часть тесты; перемещение
  прежних helper-пакетов само по себе не считается новой реализацией.
- Оценка2200–3600 сtests превышена. По условию Артёма main отправил отдельный
  async вопрос на6000–7000 total. До ответа новая реализация completed-group
  finalization/parser projection приостановлена; завершаются только проверки
  текущего diff и extraction для действующих архитектурных границ.
- Bernoulli получил proposed writer acceptedrecovery/minimal durableflow readport,
  но расширение paused до ответа. Carver parser proposal read-only; Archimedes
  завершил terminal/nativeadapter, plain/race/vet/crosscompilePASS, frozen.
- Первый handoff отдельно требует disposable launchd survival proof, независимой
  корреляции старого CLI и отдельного разрешения на importer/nonce/signals.
  Это не повторное согласование обычного deploy. Такой importer не реализован,
  live signals не отправлялись; после гибели старых владельцев допустим только
  attach-capable forward repair, не несовместимый старый binary rollback.

Integration09:12UTC:

- Все writers frozen. Controller responsibility cap сохранён5489/5500:
  existing typed/display history вынесена в controllerhistory221/250; общий
  acceptance/steer control в turncontinuation282/350. Это extraction, не новые
  функциональные обязательства; architecture/vet/scoped race PASS.
- Первый full gate: policy/format/architecture/all plain/vet/packaging PASS;
  race поймал TestStarterSteersCurrentTurnAndReturnsBeforeRootFinal с
  ErrSessionNotTracked. Main+Carver подтвердили окно: root turn публиковался
  до root wire write, steer мог обогнать submit. Точный exit43 выведен из fixture,
  не записан самим падавшим прогоном. Новый deterministic pending-wire public
  test RED написал premature steer -> GREEN не пишет; общий sent fence закрывает
  также ранний interrupt. Исходный integration test + regression race×100 PASS
  1.653s; Carver static review approve. Новый full gate запущен после исправления.
- Rollback review нашёл обход guard через interrupted transaction; RED реально
  менял pointers. Теперь signed artifact/installed validation и check-state
  предшествуют любому pointer write, включая recovery/no-op selected. Focused
  packaging14.403s PASS, review approve. State/transaction marker при отказе сохранны.
- Archimedes нашёл и удалил исключительно свой старый RED tmux-fixture orphan;
  harness cleanup исправлен cancel/join -> exact Attach -> physical Close,
  regression/race/vet PASS. Пользовательские процессы не затронуты.
- Snapshot09:11:50UTC идентичен08:56/08:18:6sessions,18inputs,history300,
  identities/cards/journal/config/plist/settings hashes прежние. Никакого push,
  deploy, user submit или ручного изменения рабочего state.
- Новое функциональное расширение остаётся paused до ответа владельца:
  completed group canonical root, повтор только local finalization, oversized
  tool projection (Carver estimate180–260production+250–400tests), первый handoff
  (Arch estimate180–300test lines плюс bounded importer после отдельного разрешения).

Physical preflight09:17UTC:

- Второй full gate после sent fence PASS, включая race runtime18.136s,
  cmd64.956s, nativeadapter16.516s, controller8.891s и executable trio.
  Но physical check-state внутри sandbox выявил скрытый побочный эффект
  OpenSessionStore: readSessionFile безусловно вызывает chmod0600. Запись была
  запрещена sandbox; это не доказательство несовместимости рабочего файла.
- RED existing0400file -> permission0600 подтвердил побочный эффект независимо
  от sandbox. Исправлено: ValidateSessionFile использует тот же strict decoder
  с repairPermissions=false; четыре штатных runtime callers сохраняютtrue.
  Профильные cmd/storage race PASS, Arch review approve. Реальный candidate
  check-state и check-config внутри исходного sandbox PASS без повышения прав.
- Для сохранения storage1900budget существующий append history перенесён в
  cardeventhistory без новой семантики и без лишнего wrapper; architecturePASS.
  Третий full gate запущен на окончательном коде, writers frozen.
- Точный текущий Go net:2116production+4339tests=6455lines. С учётом оставшихся
  parser/finalization/first-handoff оценка полного результата уточнена до7000–8000
  сtests. Она заменяет предварительный async диапазон6000–7000, ответа ещё нет;
  расширение остаётся paused по условию пользователя о согласовании масштаба.

Final handoff09:21UTC:

- Третий полный make check-full PASS на окончательном коде: policy/links/secrets/
  formatting/architecture/all plain/race/vet/operational packaging/build/trio.
  Race cmd72.625s/storage40.267s; остальные unchanged результаты кэшируются Go.
- Физический candidate20260909-persistent-terminal-candidate: --version,
  actual check-state и check-config PASS внутри исходного sandbox, без writes
  к рабочему state. Hashes:
  - bria f8190e3b3d71cb8b02a5c241b19f9a214d88eff8a40c0b6d76d5767e38dd9346
  - codex-adapter e608ae15b59dac6e81c5af7954d7ce84d614a4b811802047e7a08325ff4e9728
  - claude-adapter fe73c53af3c6fb43df327331ed3d41b8ce01e04ce32f4ca288fe6ef2bf81e5f0
- Snapshot09:21:31UTC идентичен09:11/08:56/08:18:6sessions/18inputs/history300,
  все перечисленные выше identity/card/journal/config/plist/settings hashes
  прежние. User input не повторяли, state не редактировали, service не restart.
- A31 code остаётся в origin/main4c918a3, ещё не установлен. A32 diff целиком
  local/uncommitted, никаких новых push/CI/deploy не было. Полный gate текущей
  частичной реализации не закрывает весь договор A31+A32.
- Остаток после согласования7000–8000 total: A32.1 bounded oversized tool
  projection; A32.2 canonical final root для completed root+accepted steer и
  автоматический retry только локального finalization при известном terminal;
  A32.4 disposable launchd first-handoff proof и отдельно согласованный exact
  adoption/importer, затем весь release/live boundary. Legacy unkeyed history,
  Prepared без carrier fence и уже исчезнувший старый CLI не объявлены восстановленными.
- Все агенты frozen; main интегрировал evidence. Новый ответ владельца должен
  сначала снять scale gate; прежние developer/runtime тесты не запускать заново
  до изменения кода. Старые .tmp/a31-release.rb install и hashes недействительны.

Final full-scope gate 2026-09-09:

- После явного одобрения Артёма полный объём завершён и проверен свежим
  `make check-full`: все plain/race пакеты, vet, packaging и executable trio
  зелёные; architecture registry синхронизирован с новыми границами.
- Добавлены oversized tool projection с fail-closed для неизвестных больших
  записей, canonical final root для общего NativeTurnID и retry только локальной
  финализации после terminal proof. Stale binding по-прежнему fences следующий
  member и не меняет его custody.
- Перед push/deploy нужно сохранить live snapshot и выполнить только bounded
  release sequence из `docs/ARCHITECTURE_RELEASE_PLAN.md`. Service пока старый;
  пользовательский state не менялся.
