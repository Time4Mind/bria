# Handoff: статус и следующий план

> Текущий запрос A48 - ускорить callback-проекции, восстановить page/follow
> flow, скрыть пустую New из `Фон`, добавить rename ноды и `hh:mm:ss`, исправить
> default auto-name и живой-terminal recovery. Автонейминг теперь требует от
> дешёвой модели только ASCII Latin (`A-Z`, `a-z`, цифры) и fail-closed отклоняет
> любой ответ с кириллицей или пунктуацией; валидное имя применяется до финала
> primary turn. Локально также закрыты repeatable callbacks, bounded ledgers,
> один inventory read для New/browser, durable page plan без write из
> `ProjectCurrent`, rollback-compatible activity sidecar и safe recovery cutoff.
> architecture, global race и executable trio. Первый source commit `80192c6`
> отправлен в `origin/main`: Platform Matrix `34617434528` GREEN, Stage 1
> `34617434877` выявил race выбора только что сохранённой `Starting`-сессии.
> Локально исправлена синхронизация durable row с pending FIFO; regression
> проходит 100 race-повторов, architecture GREEN. Второй A25 failure из того же
> job не воспроизведён в 1000 race-повторах и не относится к A48 path. Отдельный
> race-флейк Claude process-tree fixture устранён разделением raw-ready и PID
> handshakes; 100 повторов и полный исправленный `make check-full` GREEN. Commit
> `26a36a1` в `origin/main`, Stage 1 `34620133344` и Platform Matrix
> `34620133235` GREEN. Release preflight обнаружил прежний mutable `GO_IMAGE`;
> commit `e293bfd` закрепил официальный OCI digest и перенёс supply-chain check
> в обычный full gate. Его Stage 1 `34620999826` и Platform Matrix
> `34620999962` GREEN. Первый локальный deploy затем выявил две прежние
> operational-ловушки: public `make release` требовал внешний evidence manifest,
> а macOS service-control обращался к legacy label вместо Label точного plist.
> Локальный follow-up добавляет отдельный `release-local`, plist-derived label,
> shell regression и durable release guide. Далее - full gate/commit/exact-SHA
> CI, повторная подписанная установка и штатный restart/postflight
> `gui/501/com.time4mind.bria.v2` и safe live postflight. Полный договор и
> evidence: [UI_LATENCY_AND_SESSION_METADATA_TODO.md](UI_LATENCY_AND_SESSION_METADATA_TODO.md).

> Текущий запрос A47 - исправить открытые дефекты A46, production document
> ingress, немедленные archive/default Starting transitions и подтверждённый
> показ очищенного prompt до запуска основной модели. Первый выпущенный SHA
> `1bd0a1d` прошёл gate/CI, но live postflight выявил два дополнительных края:
> шесть старых coupled callback half-commit давали ошибку каждые 30 секунд, а
> новый перенос Codex-маркера `(p)` не распознавался автоподтверждением.
> Локально оба исправлены: stale known receipts завершаются без Telegram edit
> и UI resurrection, approval parser принимает перенос только внутри точной
> известной второй опции и по-прежнему выбирает одноразовый пункт 1.
> Точный 1,823-byte live screen сменил RED на GREEN; focused/race и полный
> `20260911-session-flow-reliability-recovery make check-full` GREEN. Далее
> exact manifest, commit/push, exact-SHA CI, restart и live postflight висящего
> approval. Дополнительный RCA ранее
> подтвердил, что shared Luna работала за 2.407 s, но её projection supersede-ил
> следующий accepted status; это ordering defect, а не отсутствие satellite.
> Локально добавлены fail-closed delivery barrier, restart output sweep,
> session-scoped archive/edit gate, Starting fallback, actual-size document
> validation, Codex+Claude Luna replacement и cancellation classification.
> Дополнительно закрыты current instruction/CLI settings, терминология создания,
> auto-name default/standby с late refresh и retry, retirement старой карточки
> фонового финала, terminal-only memory-envelope filter и production-order
> recovery фоновой Starting. Две итерации независимого review завершены approve;
> Разрешены product/tests/docs текущего репозитория и standing release;
> state/config/secrets и ручные Telegram writes исключены. Пакеты разделены:
> integration owner ведёт stale archived select и Stop/restart classification,
> отдельные owners - Luna startup diagnostics и Telegram transport classes.
> Todo и acceptance: [SESSION_FAILURE_RCA_TODO.md](SESSION_FAILURE_RCA_TODO.md).

> Текущий запрос A46 - RCA повторяющихся session/provider/recovery failures
> завершён. В актуальном release window с 12:09:36 до 13:06:58 MSK ошибок нет;
> PID6768 running, durable state содержит 7 archived + 3 ready и не содержит
> awaiting/pending/accepted. Три исторических шторма разделены: 680 recovery
> failures старого initial-start тупика исправлены `1240d77`; 302 controller
> crash + 604 recovery failures invalid callback identity исправлены `eeb4bb5`;
> 42 provider failures одного stale accepted input исправлены `583e895`.
> В текущем коде доказаны четыре открытые зоны: stale select после archive
> редактирует Telegram до failed state commit; Stop/restart ошибочно логируются
> как provider failure; Luna startup log теряет safe `thread_not_found`; Telegram
> transport redaction скрывает DNS/connect/TLS/reset class. Product/runtime не
> менялись, release не выполнялся. Договор, counts, seams и RED probes:
> [SESSION_FAILURE_RCA_TODO.md](SESSION_FAILURE_RCA_TODO.md).

> Текущий дополнительный запрос A45 - проверить сообщение о Git credentials и
> очистить источник. Actual secret в 84 reachable commit и GitHub
> secret-scanning alerts не найден. Начальный commit `a5c79ff` оставил default
> `actions/checkout persist-credentials=true`: GitHub временно помещал job token
> в runner git-config и удалял post-job. Все 10 checkout steps теперь явно
> `false`, добавлен fail-closed policy test. Также устранён повторяемый false
> positive сканера на raw bytes собранного `bin/bria` без ослабления source scan.
> Выпуск завершён: `cf5a641` в `origin/main`, полный gate и оба exact-SHA CI
> GREEN. Установлена версия `20260911-git-credential-cleanup`, PID6768 running и
> держит единственный lock; hashes, config/settings, state/history/journal
> сохранены. Итоговый identity-only `getMe` PASS. Перед ним три probe подряд
> вернули transient transport failure без HTTP auth rejection; endpoint был
> достижим, после чего проверка прошла без изменения credential. Повтор требует
> отдельного сетевого RCA, если возникнет снова.
> Договор и evidence: [GIT_CREDENTIAL_CLEANUP_TODO.md](GIT_CREDENTIAL_CLEANUP_TODO.md).

> Текущий запрос A44 - реализовать и выпустить
> порядок `запрос -> ответ` и режимы `следование / фиксированная страница`.
> Live RCA подтвердил: два Telegram-запроса `783531733`/`783531735` приняты как
> один native Codex turn, но все последующие модельные события получили turn-key
> первого запроса и были вставлены выше второго. Native rollout сохраняет
> правильный хронологический порядок. Три изолированных Luna-прогона с девятью
> steer подтвердили устойчивую границу: RPC acceptance происходит немедленно,
> старый output продолжается, затем появляется отдельный matched user record и
> только после него начинается новый model item. Значит fallback по времени не
> нужен; owner model events можно переключать по user record. Для Claude Code
> согласована та же семантика при наличии доказуемой native boundary, иначе
> append-only fallback с немедленным закреплением позиции. A44.2 обсуждает
> persisted follow/pinned plan и отдельную final-card. Live A44.2 RCA: final
> безусловно сохраняет `follow_latest=false`, даже на последней странице;
> следующий запрос наследует эту фиксацию и не следует за первой новой страницей.
> Эпизод 09:38-09:39 MSK: `8/8 false` -> `8/9 false`, затем только ручной Latest
> дал `9/9 true`. Уточнённый live-диагноз: основной ingress уже создаёт новый
> carrier на каждый input, включая steer. Дефект в том, что новая карточка может
> унаследовать старый page plan (`8/9 false` вместо `9/9 true`), а параллельный
> prompt-status может начать edit старого carrier. Цель: оставить существующий
> send, принудить новый carrier к latest/follow и fence-ить фоновые edits
> предыдущего. Final по-прежнему публикуется отдельной карточкой с началом
> ответа. A44 полностью согласован Артёмом. Реализация выполнена локально:
> exact/FIFO owner проходит через nativeadapter -> sessionruntime -> controller,
> новый input получает latest/follow, а prepared публикации сериализованы и
> повторно проверяют carrier/revision перед background edit. Профильные
> RED/GREEN и joined navigation/final тесты PASS. Полный `make check-full`
> 2026-09-11 GREEN, включая architecture, unit/integration, vet, packaging,
> race и executable-trio acceptance. Выпуск завершён: `7949654` в `origin/main`,
> Stage 1 `34580813867` и Platform `34580814028` GREEN; установленная версия
> прошла hashes, sole-lock, state/config/journal, сохранность и Telegram
> postflight. Затем A44 runtime штатно вошёл в выпущенную версию A45.
> Договор и todo: [MESSAGE_ORDER_AND_PAGE_MODE_TODO.md](MESSAGE_ORDER_AND_PAGE_MODE_TODO.md).

> Текущий запрос A43 - исправить зависший препроцессинг и порядок сообщений
> активной `workdir7`. Договор и evidence:
> [PREPROCESSING_HELD_INPUT_DIAGNOSIS_TODO.md](PREPROCESSING_HELD_INPUT_DIAGNOSIS_TODO.md).
> Проверено: свежий update `783531727` принят Telegram и сохранён `pending`, но
> до Luna не дошёл. Очередь блокирует более старый `783531721` в фазе `unknown`,
> оставшийся после успешного Luna preprocess и последующего
> `provider_error`/`adapter_failed` основной Codex-сессии 2026-09-10 18:25 МСК.
> Текущие main и shared-Luna процессы живы; дефект - startup/reconnect не
> разрешает исторический pre-acceptance `unknown`, поэтому pending 724/727 не
> lease-ятся. Артём выбрал правило: после доказанного общего сбоя все
> незавершённые запросы, существовавшие на границе успешного recovery, навсегда
> skip-ятся независимо от статуса и не replay-ятся; новые запросы идут дальше
> по порядку. Обычная живая очередь без recovery не меняется. Реализация A43
> закрывает exact-acceptance race, атомарность backup, строгую проверку journal,
> синхронный recovery finalizer и fail-closed rollback несовместимого бинарника.
> Ручной state edit/replay запрещён; после полного gate действует standing
> release sequence.
> Первый live postflight `a13d5ae` подтвердил `workdir7` Ready и exact
> 721/724/727=`skipped`, но выявил старую `d61...` с open cutoff и древним
> `accepted`. Финальное уточнение A43: при доказанном total failure старый
> accepted-turn также не наблюдается и не оживляется, а skip-ится вместе со
> всем captured prefix. Регрессионный тест и полный v2 gate PASS; второй release
> выполняется автоматически.

> Текущий запрос A42 - определить происхождение live-сессии `ab396f5b...` и
> исключить устойчивый статус `awaiting_recovery`: ранее открытая и физически
> доступная сессия должна автоматически восстановиться, доказанно
> невосстановимая - закрыться/архивироваться. Ручная правка state и replay
> accepted input запрещены. Договор, coverage и todo:
> [AWAITING_RECOVERY_TERMINAL_STATE_TODO.md](AWAITING_RECOVERY_TERMINAL_STATE_TODO.md).
> RCA подтверждён: exact tmux/Codex `workdir7` жив, но старый journal `unknown`
> без native receipt отменял уже успешный attach. Фикс сохраняет replay fence,
> но не блокирует reconnect/новый pending; доказанно отсутствующий terminal
> архивируется через bounded safe class. Shared и per-session satellite restart
> exact-resume-ят прежние thread ID; archived personal не запускается. Полный
> `20260910-terminal-recovery make check-full` PASS с каноническим `/private/tmp`.
> Runtime `e9e8fa1` прошёл оба exact-SHA CI и live восстановил `workdir7` в
> `ready` с прежним thread и живыми tmux/Codex. Postflight выявил отдельную
> границу: прогретый, но ни разу не использованный shared satellite получил ID,
> однако Codex ещё не создал rollout, поэтому exact resume вернул доказанный
> `thread not found`. Добавлен узкий replacement только для этого класса.
> Классификация проходит через валидируемый `startup_failed/thread_not_found`
> frame до остановки process tree; stderr-вариант отклонён реальным
> process-boundary тестом. Focused plain, race, architecture и metadata-only
> live Codex test PASS; transient/network/auth сохраняют старый binding.
> Финальный выпуск завершён: `067a508f` в `origin/main`, полный
> `20260910-terminal-recovery-satellite-v2 make check-full` и exact-SHA CI
> Stage 1 `34529896838` / Platform `34529897002` PASS. Установлен versioned
> release, PID46682 running/sole process; hashes/config/settings/getMe проверены.
> `workdir7` ready, сохранила exact provider thread и живые terminal/Codex PID,
> generation4→5; stable awaiting среди активных сессий нет. Shared Luna
> автоматически заменила доказанно отсутствующий empty rollout и стала ready с
> новым binding `01a08d25...`; queue 31 inputs сохранена. Per-session exact
> active reconnect, archived stop и FIFO проверены automated restart/race,
> live-настройка не переключалась. A42 закрыт; повторный deploy не нужен.

> A41 выпущен: старая one-shot схема препроцессинга заменена постоянными
> скрытыми Codex/Luna-сателлитами с режимами `disabled`, `shared` и
> `per_session`; default и миграция старого `true` - `shared`. Реальный Luna E2E
> подтвердил общий thread для двух primary и exact resume, lifecycle archive и
> UI invisibility закрыты тестами. Source `b573960` в `origin/main`, полный gate
> и оба exact-SHA CI PASS. Установлен
> `20260910-persistent-preprocess-satellites`, PID47217 running/sole lock;
> hashes/config/state/Telegram identity и сохранность 8 sessions/31 inputs
> подтверждены. Shared Luna ready за 1205ms. Один стартовый recovery failure
> ранее проблемной сессии не повторился за 2 минуты благодаря stable evidence
> barrier. Ручной Telegram prompt не отправлялся. Договор и release receipt:
> [PER_SESSION_PREPROCESS_SATELLITE_TODO.md](PER_SESSION_PREPROCESS_SATELLITE_TODO.md).

> Текущий запрос A40 - одна скрытая готовая Luna/cheap-model session для
> препроцессинга, immediate replacement после lease и close старой только после
> durable acceptance. Договор, coverage и todo:
> [PREPROCESS_SESSION_POOL_TODO.md](PREPROCESS_SESSION_POOL_TODO.md).
> Выпуск завершён: runtime `84d1e4e`, полный локальный gate и оба exact-SHA CI
> PASS. Установлен `20260910-preprocess-session-pool`, PID 7310 running/sole
> lock. После restart Luna-сессия перешла `starting` -> `ready` за 955 ms;
> свежих failed/error/critical нет. Сохранены 8 sessions, 8 cards, 557 history,
> 13 journal sessions и 28 inputs; config/settings hashes не изменились,
> `check-config` и Telegram identity OK. Synthetic Telegram input не отправлялся.

> Текущий запрос A39 - исправление задержки `Новое`, failed создания и
> всех доступных recovery-эпизодов. Последняя session сохранена, но после
> `adapter_failed` осталась `awaiting_recovery` без binding и без auto-retry;
> активной осталась прежняя session. Внутренний путь `Новое` занял менее 0.7 s,
> click-to-ingress не измеряется. История содержит 682 recovery failures, включая
> 680 записей двух ошибок на каждом 10-second sweep. Артём разрешил product fixes:
> bounded initial retry + safe empty cleanup, capped recovery backoff, safe
> startup stage logging и UI consistency; после full gate действует standing
> release sequence. Todo/evidence:
> [SESSION_CREATION_RECOVERY_DIAGNOSIS_TODO.md](SESSION_CREATION_RECOVERY_DIAGNOSIS_TODO.md).
> Initial retry, config-aware live recovery для `starting`/unbound awaiting,
> lifecycle backoff, safe session-correlated startup logs, native cleanup и UI
> selection regressions выпущены. Runtime `1240d77`, полный локальный gate и оба
> exact-SHA CI PASS. Установлен `20260910-session-recovery`, PID 20745
> running/sole lock; hashes и пользовательские данные сохранены. Проблемная
> `ab396f5b...` автоматически перешла из unbound awaiting в Ready с binding,
> свежих critical events после `telegram.flow_ready` нет. Ручной новый Telegram
> input не отправлялся; click-to-ingress по-прежнему не измеряется.

> Текущий запрос A38: ускорить полный выпуск Bria без сокращения runtime/race/
> platform/policy проверок. Fail-closed release-journal routing, Go cache и
> параллельный Stage 1 выпущены по
> [CI_ACCELERATION_TODO.md](CI_ACCELERATION_TODO.md). Source `cfc093e`, полный
> local gate и оба exact-SHA CI PASS; Stage 1 сократился с 4:01-4:25 до 2:31.
> Установлен `20260910-ci-acceleration`, PID36052 running/sole lock; hashes,
> config/settings и 7 сессий/7 карточек/553 history сохранены. Этот docs-only
> receipt проверяет лёгкий маршрут; повторный deploy не нужен.

> Объединённый выпуск A35+A36 установлен. Status после первого клика
> автоматически публикует свежую квоту; Ноды/Статус теперь сохраняют подпись
> `Назад`; активная карточка форматирует многострочные exec-команды нативным Rich
> code-блоком отдельно от вывода. Вложенные backticks, пагинация, лимиты 20+20,
> approval/notification wire и focused race проверены. Commit `7ce4578`, полный
> `VERSION=20260910-status-formatting make check-full` и оба exact-SHA CI PASS.
> Версия `20260910-status-formatting`, PID51199 running/sole lock; хэши тройки,
> сохранность 7 сессий/7 карточек/553 history и неизменность config/settings
> подтверждены. Визуальный Telegram-флоу и one-click Status остаются на следующую
> обычную пользовательскую проверку без тестового сообщения от агента.
> Договор/evidence: [STATUS_REFRESH_DIAGNOSIS_TODO.md](STATUS_REFRESH_DIAGNOSIS_TODO.md).

> A35 исправлен локально: кешированная карточка сначала подтверждается Telegram,
> затем один background poll автоматически редактирует только ту же всё ещё
> текущую signed Status-проекцию. Повторные клики используют один poll, а уход в
> меню/сессию блокирует поздний edit. Public RED/GREEN, 10x race и architecture
> gate и финальный versioned `make check-full` PASS; safe `quota.refresh` timing
> не содержит значений лимитов. Далее push/CI, установка, restart и live
> one-click проверка.
> Первая CI-итерация: Stage 1 `34402279412` PASS; Platform `34402279296`
> выявил независимую readiness/abort race в sessionruntime на macOS. `Start`
> мог вернуть binding в коротком окне после удаления process record, но до
> закрытия `done`. Добавлена проверка exact tracked record под тем же lock;
> 10 000 stress и 2 500 race interleavings, затем полный versioned gate PASS.
> Далее follow-up SHA и повтор обоих CI.
> Договор/todo: [STATUS_REFRESH_DIAGNOSIS_TODO.md](STATUS_REFRESH_DIAGNOSIS_TODO.md).

> A34 закрыт: generation хранит набор command-derived uncertain DecisionID,
> поэтому старый запрос не replay-ится, а distinct approval проходит. Runtime
> `d4d5430` в origin/main, полный versioned gate и оба exact-SHA CI PASS.
> Установлен `20260909-approval-restart-stall`, PID41157 running/sole lock,
> hashes и сохранность 7 сессий/7 карточек подтверждены. Активная `workdir6`
> сохранила provider session и без ручного ввода приняла исходный зависший плюс
> ещё четыре разных approval в generation5. Пять safe lifecycle-событий не
> содержат command/reason payload; Telegram identity OK, свежих errors нет.
> Исторические recovery errors не повторились в release window. Договор/receipt:
> [APPROVAL_RESTART_STALL_TODO.md](APPROVAL_RESTART_STALL_TODO.md).

> GitHub Issue #1 исправлен: Codex quota collector удаляет session-only bypass
> только из собственной копии argv; рабочая конфигурация и экраны Ноды/Статус
> не менялись. Runtime `f67434d` в origin/main, полный gate и оба CI PASS.
> Установлен `20260909-codex-quota-bypass`, PID4746 running/sole lock; hashes,
> state preservation и реальный Codex quota RPC подтверждены. Договор и receipt:
> [GITHUB_ISSUE_1_QUOTA_BYPASS_TODO.md](GITHUB_ISSUE_1_QUOTA_BYPASS_TODO.md).

> Follow-up R6: после выпуска RootReady live-проверка выявила второй блокер.
> Startup attach зарегистрировал observer старого accepted558, сохранил gen3
> Running и отложил новый pending603. Исправляется eligibility: accepted history
> уступает только более новому pending, session возвращается Ready; unknown/failed,
> close intent и accepted без successor сохраняют прежний барьер. Договор/todo -
> [ARCHIVE_RESUME_DIAGNOSIS.md](ARCHIVE_RESUME_DIAGNOSIS.md). После полного gate
> автоматически push/CI/deploy и live-проверка без ручной правки state/terminal.
> Код и изолированный policy-пакет готовы; RED/GREEN, focused race и
> полный `make check-full` PASS 13:53 UTC. Candidate
> `20260909-archive-successor-unblock`, Bria hash `f78b48bd...`. Выпуск
> и физическая приёмка завершены: source `0e78f26` в origin/main,
> оба CI PASS, релиз установлен, PID16335 running/sole lock. Pending603
> стал accepted, затем completed с нормальным final; сессия Ready.
> Старый accepted558 не replay-ился. Сохранены 6 сессий, 6 карточек,
> 337 history, 19 inputs, settings/config; Telegram identity OK, свежих
> critical/error после flow_ready нет. R6 закрыт; repeat deploy не нужен.
> Docs-only receipt699bf2e выявил независимую CI-race: PID helper публиковал
> файл неатомарно, reader иногда видел пустое значение. Test-only fix
> публикует PID через atomic rename; race stress100/100 и полный
> `make check-full` PASS. Runtime/hash/service не изменились, repeat restart не нужен;
> final CI follow-up коммита проверяется live.

> Повторная приёмка: владелец восстановил workdir5, новый запрос был сохранён,
> Проверено12:27UTC: ready/gen2; новый783531603 сохранён pending, старый558 accepted.
> Public RootReady на live-snapshot возвращал false: отдельный admission guard
> блокировал новый root после успешного archive reopen. Фикс F5: accepted не
> блокирует следующий root, старый accepted не leaseable; unknown/failed
> сохраняют защитный барьер. Полный gate PASS, выпуск в процессе. Диагноз:
> [ARCHIVE_RESUME_DIAGNOSIS.md](ARCHIVE_RESUME_DIAGNOSIS.md). В этом ходе только
> state не менялся вручную; pending-запрос проверю после выпуска.

> Текущий запрос: исправить явное восстановление из архива при accepted-запросе
> без terminal receipt и убрать техническое сообщение о потере связи из карточки.
> Договор, проверенная причина и текущий todo:
> [ARCHIVE_RESUME_DIAGNOSIS.md](ARCHIVE_RESUME_DIAGNOSIS.md).
> Исправление archive guard прошло RED/GREEN и профильные race-тесты;
> интеграция карточки и полный make check-full завершены успешно.
> Выпущено 2026-09-09 в12:09UTC: runtime42f0684, оба CI и полный gate PASS.
> Версия20260909-archive-resume-quiet, PID44368 running/sole lock; hashes,
> Telegram getMe и сохранность6 сессий/300 history/18 inputs подтверждены.
> Восстановление из архива исправлено без replay; технический disconnect-текст
> скрыт. Также устранена ошибка порядка карточки при мгновенном ответе.
> Реальное нажатие владельцем «восстановить» после выпуска не проверено.
> Промежуточные причины/статусы ниже исторические; новый deploy не требуется.

> Текущий запрос A31+A32: скрытые каталоги с настройкой плюс автоматическая
> привязка к живому терминалу/восстановление без действий владельца и исправление
> carrier race после голосового. Единый договор/todo/coverage:
> [HIDDEN_DIRECTORIES_TODO.md](HIDDEN_DIRECTORIES_TODO.md), раздел A32.
> A31/C1 код в origin/main4c918a3, full gate и оба CI PASS; ещё не установлен.
> A32 exact compaction root и carrier/callback race исправлены локально;
> RED/GREEN, независимое review, full make check-full и физический candidate PASS.
> A32.2 full разрешён ответом Артёма «Да»: сохранение того же терминала и
> attach-only после restart реализуются параллельно по карте в едином todo.
> Предыдущие fixes проверены; новый full lifecycle ещё проходит реализацию.
> Scope estimate превышен: текущий Go net6455, две третиtests; уточнённый полный
> объём7000–8000 ожидает подтверждения. До ответа parser/finalization paused,
> текущие проверки/extraction продолжаются. Первый handoff также требует
> отдельного безопасного плана/разрешения; обычный restart старого binary опасен.
> Final current partial candidate прошёл make check-full2026-09-09~09:21UTC,
> actual check-state/check-config read-only PASS; hashes и незакрытые пункты вtodo.
> Writers frozen. Реализация всего A31+A32 не завершена; выпуск не выполнялся.
> A32 diff не committed/pushed/installed.
> Общий oversized scope, legacy Prepared fence, первый handoff и rollback старого
> strict-state binary остаются открытыми; все границы и hashes в едином todo.
> Старый сервис PID18322, source dcb09d8; в07:08:52UTC после oversized native
> transcript record одна сессия awaiting_recovery, accepted input не повторять.

> A30 и CI-fix C1/C2 выпущены: базовый промпт дословно заменён на текст Артёма;
> исправлены ошибочный timing-test и ложная критическая ошибка штатной отмены
> expiry. Настоящие и смешанные ошибки не скрываются, cmd assertions не ослаблены.
> Закрытый todo/evidence - [PREPROCESS_PROMPT_TODO.md](PREPROCESS_PROMPT_TODO.md).
> Sourcedcb09d8 в origin/main, RED/GREEN/full gate и оба CI точного SHA PASS
> (34318996509,34318996449). В06:30UTC 2026-09-09 установлен speech-cleanup-shutdown;
> PID18322 running/sole lock, hashes/exact prompt/getMe/startup recovery OK.
> Сессии, история, journal и настройки сохранены, custom инструкции не менялись.
> Preprocessing включён с пустой инструкцией: новые запросы берут новый default.
> Ошибок в проверенном стартовом окне нет. Живая модель на качество переписывания
> не тестировалась. Эта запись docs-only; повторный deploy того же кода не нужен.
> Обязательства A30/C1/C2 закрыты, прежние промежуточные статусы ниже исторические.

> A29 выпущен: навигация не перерисовывает меню/Ноды, последняя страница следует
> за генерацией, финал публикуется новой карточкой с началом ответа.
> Весь закрытый todo и evidence - [NAVIGATION_FOLLOW_TODO.md](NAVIGATION_FOLLOW_TODO.md).
> Источник6eba913 в origin/main; полный make check-full, независимые review и оба
> CI точного SHA PASS (34314902178,34314902107). В05:33UTC 2026-09-09 установлен
> 20260909-navigation-follow; PID23498 running/sole lock, hashes и Telegram getMe OK.
> Стартовые flow_ready/recovery подтверждены, ошибок в проверенном окне нет.
> Сохранены все5 сессий,5 карточек/146 записей истории,16 completed inputs,
> settings/config/journal; четыре прежних unknown delivery не менялись.
> AGENTS и тесты защищают автоматические commit/push/merge/deploy без повторного
> согласования после готовой разработки. Direct main не требует фиктивного merge.
> CI-fix изменил только нестабильный heartbeat negative control, не runtime.
> Финалы сверх лимита владелец исключил; лимит не изменён и случай не исправлен.
> Ручной визуальный Telegram UI не проверялся; автоматическая wire-приёмка прошла.
> Эта запись поставляется docs-only коммитом; повторный deploy тех же бинарников
> не нужен. Старые открытые статусы ниже исторические; текущих блокеров A29 нет.

> A26+A27+A28 выпущены: раздельные лимиты, сохранение принятого запроса и общий
> таймер текста/скриншота с reset на действия владельца. Код b65c6f6 в origin/main,
> полный make check-full и оба обязательных CI PASS. В 22:04 UTC 2026-09-08
> установлен separate-spoiler-limits; PID90706 running/lock, Telegram getMe OK.
> Сохранены 5 сессий, 146 записей истории, 16 completed inputs и все настройки.
> В свежих логах flow_ready/recovery, ошибок нет. Receipt и весь закрытый todo -
> [SPOILER_LIMITS_TODO.md](SPOILER_LIMITS_TODO.md). Ручной визуальный UI не проверен.
> Следующий docs-only commit фиксирует этот receipt; повторный deploy не нужен.

> Текущий запрос A26+A27: независимые лимиты спойлеров и сохранение принятия
> моделью при потере процесса, перезапуске и разархивации. Весь договор и todo -
> [SPOILER_LIMITS_TODO.md](SPOILER_LIMITS_TODO.md). Вся версия прошла финальный
> make check-full и независимое ревью 2026-09-08 21:38 UTC; физическая сборка проверена.
> Выполняется разрешённый push/CI/install/restart с postflight. До его receipt
> работающий сервис остаётся на final-save-retry; готовность кода не равна выпуску.

> A26 release разрешён: новое явное указание владельца - после готовой доработки
> выполнять push/CI и перезапуск Bria автоматически, без повторного запроса.
> Точный договор/цели/приёмка - amendment в SPOILER_LIMITS_TODO.md.
> Обновляются AGENTS и защита правил; далее полный gate и выпуск с postflight.

> A26 локально выполнен: отдельные лимиты строк команды и вывода 3/5/10/20.
> Договор, владение и todo: [SPOILER_LIMITS_TODO.md](SPOILER_LIMITS_TODO.md).
> RED/GREEN, профильные race, signed callback/Rich wire, миграция и независимое
> ревью пройдены; policy/architecture/vet и сборка separate-spoiler-limits PASS.
> Первоначальный local-only scope заменён последующим разрешением выпуска выше.
> Предыдущий выпуск завершён; его исторические открытые статусы ниже не актуальны.

> После документирования выпуска CI938f247 обнаружил нестабильность heartbeat
> теста остановки вложенного Claude-процесса (timestamp -> пустой файл).
> Работающий код не менялся:00b26a7 прошёл оба CI и установлен; сервис здоров.
> Test-only исправление фикстуры завершено: атомарный counter, bounded quiescence,
> отрицательный контроль живого writer; review approve и полный gate PASS19:13 UTC.
> Все три rebuilt SHA совпадают с установленными; новый перезапуск не нужен.
> Старый красный run относится к938f247; актуальные remote checks нужно читать
> для коммита с исправленным тестом, не переносить на него старый CI-статус.

> Revision7 выпущен: source00b26a7 в origin/main, оба GitHub CI PASS. В18:59 UTC
> установлен20260908-final-save-retry и перезапущена только Bria. НовыйPID89675
> держит lock, версия/хеши совпадают, Telegram getMe OK. Сохранены5 сессий,
> 5 карточек/135 записей истории/15 completed inputs, конфиг и настройки.
> Стартовое recovery успешно: теперь3 ready/2 archived, awaiting_recovery нет.
> Свежих ошибок в проверенном окне нет. Доказательства - revision7 release receipt
> в ARCHITECTURE_RACE_TODO.md. Это завершение текущего запроса, не новый backlog.
> Не проверены вручную внешний вид карточки и искусственный отказ диска в live;
> запросы модели/тестовые сообщения не отправлялись. Ниже сохранена история выпуска.

> Выпуск revision7: 4d82381 опубликован в origin/main, локальный полный gate PASS.
> GitHub Stage1 PASS, но macOS native CI выявил гонку выхода исполнителя и чтения
> буферизованных событий. Исправление в рамках того же выпуска завершено локально:
> барьерный stress100/100 PASS, шесть exit-drain сценариев, независимое review
> approve и новый полный make check-full PASS 18:54 UTC. Готовится follow-up push/CI;
> установка/перезапуск ещё НЕ выполнены, current остаётся 20260908-card-order.
> Не скрывать красный CI повторным запуском без проверки причины. Текущий follow-up
> и владение - revision7 в ARCHITECTURE_RACE_TODO.md; поручение выпуска сохраняется.

> A25 revision 7: автоматический повтор записи финала реализован; профильные
> проверки PASS. Повторяется только сохранение, не запрос модели; до успеха очередь ждёт.
> Проверены transient/ambiguous/persistent, main+steer/B-once, shutdown/stale,
> физическая запись и корреляция JSONL без private payload. Финальный `make check-full`
> PASS 2026-09-08 18:40 UTC, независимые review approve, три Mach-O arm64 бинарника
> проверены. Config, settings и копии state/journal читаются.
> Договор/владение/todo - revision 7 в ARCHITECTURE_RACE_TODO.md.
> Условное поручение пуша и перезапуска после полной готовности сохраняется.
> Точные цели: Time4Mind/bria main, gui/501/com.time4mind.bria.v2,
> три бинарника 20260908-final-save-retry; без ручной правки конфига и данных.
> Старое ожидание решения по A25.F1 ниже больше не действует.

> Новое указание: пуш и перезапуск разрешены при условии, что весь план завершён.
> Проверка текущего кода подтверждает остаток A25.F1: после отказа записи финала
> нет повторного сохранения без перезапуска живого исполнителя. A25.F2 завершён,
> но нельзя выдавать это за отсутствие всех ограничений. Выпуск пока не выполнен;
> нужно решение по F1. Источник правил подтверждения - AGENTS.md, не память.
> Договор и статусы A25.R1-R3 - текущий release condition audit в todo.
> Фраза ниже о необходимости отдельного согласования историческая: последнее
> сообщение уже содержит условное разрешение, его нельзя игнорировать.

> A25 revision 6 локально завершён: подтверждённый failed/interrupted закрывает
> прежний запрос и пропускает следующие без replay; unknown и недоказанные/старые
> failed остаются заблокированными. Новый запрос ждёт записи исходов main и всех
> steers; отказ сохранения оставляет неотправленное сообщение Pending.
> Два независимых review approve, полный `make check-full` PASS; физическая сборка
> `20260908-terminal-queue`, darwin/arm64, три бинарника проверены.
> Договор/todo/доказательства/хеши - revision 6 в ARCHITECTURE_RACE_TODO.md.
> Сервис не менялся: symlink reread 2026-09-08 17:17 UTC - `20260908-card-order`.
> Commit/push/install/restart/live не выполнялись и требуют отдельного согласования.
> A25.F1 остаётся отдельным ограничением live retry записи финала. A25.F2 закрыт
> локально; ожидание решения в исторической revision 5 ниже больше не действует.

> A25 revision 5: согласованные parser/spoiler/settings/recovery/UI/logging/voice
> доработки локально реализованы; независимое review и `make check-full` PASS.
> Физическая сборка `20260908-recovery-tool-limits`, darwin/arm64, три бинарника.
> Хеши и полный договор/todo - revision 5 в ARCHITECTURE_RACE_TODO.md.
> Сервис не менялся: symlink reread 2026-09-08 16:20 UTC всё ещё `20260908-card-order`.
> Push/install/restart/live recovery не выполнялись и отдельно не разрешены.
> Две явные границы: A25.F1 - при отказе записи финала у живого provider нет live
> retry истории, сохраняется Unknown без replay; A25.F2 - прежний failed блокирует
> очередь без UI retry, изменение на продолжение следующих сообщений после
> известного terminal вынесено Артёму на решение, ответа пока нет. Согласованные
> блоки не сокращены; новые ограничения не считать молча исправленными. Следующий
> шаг - решение A25.F2; deploy требует отдельного bounded подтверждения.

> A25 revision 4: последний voice блок принят. Все требования согласованы;
> оценка выявила риск большой доработки recovery: receipt содержит лишь исход,
> не финал/восстановление карточки. Предложен минимальный этап без НОВОГО
> восстановления финала по кнопке; это частичное отложение A25.5/A25.6 требует ОК.
> До решения возобновление product-кода не выполнялось.
> Частичные правки НЕ готовы к выпуску. Договор/todo - A25 revision 4 в
> ARCHITECTURE_RACE_TODO.md. Deploy/restart/push/live recovery не разрешены.

> A25 revision 3: требования парсера и recovery visibility приняты явно;
> unknown outcome принят с согласованием ДО потенциально большой переработки.
> Logging принят при проверке полноты существующей цепочки; она ещё не проверена.
> Сейчас идёт согласование разделов, product implementation остаётся на паузе.
> Правило реакции агента на повторяющиеся ошибки - раздел «Повторяющиеся ошибки
> в логах» в HARNESS.md. Актуальные договор/todo - A25 revision 3 ниже по ссылке
> [ARCHITECTURE_RACE_TODO.md](ARCHITECTURE_RACE_TODO.md). Старые ограничения
> scope ниже - история, не отменяющая последующие явные решения. Сервис не менялся.

> A25 scope clarification: явно подтверждены только правила спойлера/настройки
> строк. Начатая локальная правка парсера приостановлена до отдельного согласования;
> остальные recovery/UI/logging блоки не приняты автоматически. Есть незавершённые
> локальные изменения; выпуск/полная приёмка НЕ выполнены, сервис не менялся.
> Ниже исходное расширенное толкование A25 сохранено как история, не разрешение.
>
> A25 первоначальный план: блок больших технических событий и отображения.
> Локально: устойчивое bounded чтение native журнала; спойлер максимум 100
> Unicode-символов в строке, лимит после переноса 5/10/20/40 (default 10),
> читаемые text blocks, отдельная отметка сокращения. Без установки/restart.
> Договор/владение/todo A25 в ARCHITECTURE_RACE_TODO.md. Остальные блоки A24
> остаются для отдельного согласования, неизвестный prompt не переотправлять.

> A24 диагностика завершена: native запись 2,32 МБ превышает лимит Reader 1 МиБ;
> exact read-only replay воспроизводит ErrLimit. Bria закрывает адаптер/CLI,
> сессия остаётся awaiting_recovery: кнопка скрыта, активная карточка сохранена.
> Большой текст - точно сопоставленные tool metadata при включённом их показе.
> Перенос голосового уже исправлен A21; новый сквозной pending-voice Rich wire
> regression и 10 race повторов PASS. Production не менялся. Сервис всё ещё
> `20260908-card-order`; локальные исправления не установлены. Новые parser/UI/
> logging fixes, установка и восстановление сессии требуют согласования.
> Договор/todo и evidence A24 в ARCHITECTURE_RACE_TODO.md; внешних writes нет.

> A23 локально выполнен: после принятой архивации выбирается другая доступная
> сессия либо список сессий; работа занятой сессии продолжается в фоне до конца.
> Исправлены потерянное async completion, гонки повторного закрытия и сохранения
> выбора; schema 3 связывает close, fallback, persist, projection и card delivery.
> Public RED/GREEN, сквозной state/wire/JSONL, race и независимое ревью пройдены.
> Финальный `make check-full` PASS, физическая сборка `20260908-archive-switch`.
> Договор/todo/хеши - A23 в ARCHITECTURE_RACE_TODO.md. Установка, push и перезапуск
> не выполнялись; установленная версия по reread всё ещё `20260908-card-order`.
> Live-повторение владельцем возможно после отдельно согласованной установки.

> A22 revision 3 локально выполнен: новые доставки сохраняют точные Rich-страницы
> до отправки; начатые продолжаются по прежним частям, confirmed/unknown не
> сбрасываются. Проверены конкуренция процессов, crash/reopen, HTTP 408,
> неизменность input и отказ при потере плана. Независимое ревью approve; финальный
> `make check-full` PASS, собрана и проверена `20260908-saved-page-plans`.
> Договор/доказательства - A22 в ARCHITECTURE_RACE_TODO.md; эксплуатационные
> ограничения - [NOTIFICATION_PAGE_PLANS.md](NOTIFICATION_PAGE_PLANS.md).
> Сборка не установлена, push/restart/live сообщения не выполнялись. Установленный
> symlink по reread всё ещё `20260908-card-order`. После перехода файла состояния
> на формат v2 нельзя откатывать только бинарник на версию без поддержки планов.
> Ниже revision 2 - история предыдущей локальной сборки; прежнее ожидание
> согласования A22.6 закрыто явным решением Артёма и текущей реализацией.

> Текущий запрос A22 (2026-09-08): проверить агентом рендер таблиц в
> Bria Legacy и воспроизвести его в текущей Bria. Legacy - только исторический
> источник по явному разрешению. Конфликт с текущими требованиями сначала
> согласовать с Артёмом табличным сравнением; до решения не реализовывать
> конфликтующий вариант. Deploy/push/restart/live messages не разрешены.
> Договор и todo: [ARCHITECTURE_RACE_TODO.md](ARCHITECTURE_RACE_TODO.md), A22.
> Дополнение владельца, revision 2: Rich Markdown во всех исходящих текстовых
> сообщениях Bria, включая меню/статусы/уведомления; не только в таблицах.
> Локальная реализация проверена: все send/edit текстов идут через Rich,
> таблицы в карточках сохраняют заголовки на продолжениях, literal code не
> превращается в таблицы. Старый entity-format тракт удалён как неиспользуемый;
> A21 переносы/изоляция финала сохранены. Полный `make check-full` PASS,
> физическая сборка `20260908-rich-messages`; независимое ревью завершено.
> A22.6 требует согласования: длинные уведомления пока сохраняют прежнее
> разбиение ради уже сохранённых подтверждений частей. Новый общий paginator
> нельзя подставлять без совместимого плана старых/новых доставок.
> Live-визуализация не проверена, сборка не установлена, push/restart не было.
> Установленный symlink по повторному чтению - `20260908-card-order`.

> Текущий запрос A21 (2026-09-08): ровно один видимый перенос после
> разделителя заголовка и отдельные страницы финального ответа любой длины.
> Локально выполнен: RED/GREEN, сквозной JSON wire, race, независимое ревью
> и полный `make check-full` прошли. Собрана `20260908-card-layout`.
> Финал и продолжения изолированы; исправлены лишние разделители внутри
> длинного финала. Новый deploy, перезапуск, push и обновление live-карточки
> не выполнялись и требуют отдельного разрешения. Текущая установленная
> сборка по reread symlink - `20260908-card-order`. Договор и todo:
> [ARCHITECTURE_RACE_TODO.md](ARCHITECTURE_RACE_TODO.md), раздел A21.

> A20 выполнен (2026-09-08, 13:19 МСК): Bria обновлена до
> `20260908-card-order`, история затронутой сессии восстановлена без потери
> всех 55 записей. Существующая Telegram-карточка обновлена с подтверждением
> возвращённого текста, финал находится на последней странице. Codex-сессия
> та же, задача не перезапускалась; остальные карточки не изменены.
> PID 89657, lock/getMe/новые логи проверены. Ниже A19/A17 - история этапов,
> прежние ожидания разрешения закрыты A20. Новый push не выполнялся.
> Название продукта - Bria; `v2` остаётся только техническим суффиксом
> установленных путей и launchd label, не отдельным проектом.

> Текущий запрос A19 (2026-09-08): исправить порядок событий карточки после
> подтверждённого A18 дефекта потери `HistoryTurnKeys`. Локальная реализация,
> регрессионные тесты и сборка; deploy, перезапуск, push и восстановление
> уже испорченной live-истории отдельно не разрешены. Договор и todo:
> [ARCHITECTURE_RACE_TODO.md](ARCHITECTURE_RACE_TODO.md).
> A19 локально выполнен: порядок и следующий prompt исправлены; red/green,
> 10 повторов сквозного race-теста и итоговый make check-full прошли.
> Собрана `20260908-card-order`; сервис всё ещё использует
> `20260908-card-diagnostics`. Ожидается отдельное согласование установки;
> уже перепутанная история не перестраивалась, push не выполнялся.

> Текущий явный запрос (2026-09-08): A17 - безопасное диагностическое
> логирование кнопок/обновления карточки, затем перезапуск только Bria v2.
> Саму логику пагинации пока не исправлять; новый push не запрошен.
> Предыдущие исправления архитектуры опубликованы и установлены. Договор, владение и приёмка:
> [ARCHITECTURE_RACE_TODO.md](ARCHITECTURE_RACE_TODO.md). Предыдущий backlog
> и приведённый ниже исторический снимок сохранены, но не доказывают состояние
> текущей версии. Контекст approval-сценариев: [LUNA_APPROVAL_TODO.md](LUNA_APPROVAL_TODO.md).
> Схема диагностических событий: [CARD_DIAGNOSTICS.md](CARD_DIAGNOSTICS.md).
> A17 выполнен: полный локальный выпускной набор прошёл; сборка
> `20260908-card-diagnostics` установлена 2026-09-08 в 11:20 МСК.
> PID 46960, lock/getMe проверены; новый logger-ready и 12 событий карточки
> физически прочитаны из рабочего JSONL. Изменения не отправлялись в Git.
> Следующий шаг - повторение пользователем пагинации; сам дефект не исправлялся.

> Актуальный указатель (2026-09-06): восстановление описано в
> [HARNESS.md](HARNESS.md). На текущей машине команда контекста открывает
> `bria-stabilize` со всеми ограничениями и пунктами S1–S5, затем показывает
> отдельный сохранённый backlog. Полная актуальная приёмка стабилизации не закрыта;
> старые успешные проверки и блокеры требуют сопоставления с текущей версией.
> Поздние явные решения владельца определяют действующий объём. Исправление
> Harness не закрывает продуктовые задачи и не разрешает запускать весь backlog.

Снимок этого документа: **2026-09-03, Europe/Moscow**. Это handoff только для
текущего репозитория Bria. Документ не является разрешением на публикацию,
выпуск или внешние записи.

## Короткий вердикт

Этап 2 продолжается; production готовность не заявляется. На текущем снимке
есть проверяемый локальный single-machine путь `bria run` для координатора,
сессий Codex/Claude и Telegram controller, а также отдельные компонентные
границы для медиа, файлов, backup/restore, update/rollback, observability и
multi-computer. Полный внешний пользовательский путь и финальные release gates
не пройдены.

Особенно важно: локальная wiring artifact delivery, turnruntime, P4 и
observability теперь подключена и scoped-green. Локальный
architecture/full-suite gate закрыт; внешний acceptance и снятие
архитектурных ограничений для network roles остаются **pending**.

## Состояние доказательств

### Проверено в текущем репозитории

- `go.mod` задаёт Go 1.22; основной CLI - `cmd/bria`, рядом с ним собираются
  `bria-codex-adapter`, `bria-claude-adapter` и container preflight.
- `cmd/bria run` загружает и строго валидирует конфигурацию, проверяет Telegram
  identity перед открытием состояния, захватывает instance lock и запускает
  single-machine controller.
- В локальном пути присутствуют durable state, message journal, owner/chat
  gate, Telegram controller/bridge/flow, signed callbacks, session lifecycle,
  recovery/supervision, provider interactions, API-key authorization boundary
  и safe-log cleanup.
- Отдельными тестами покрыты domain/session rules, очередь и journal,
  Codex/Claude adapters, process groups, recovery, callbacks, media custody,
  artifact retry, backup/restore, update/rollback, observability и
  multi-node protocol boundaries. Это доказательство кода и component tests,
  а не внешний acceptance.
- Текущая локальная wiring проверена командой:
  `go test -mod=readonly ./internal/artifactretrycomposition ./internal/artifactruntimecomposition ./internal/turnruntimecomposition ./internal/p4runtimecomposition ./internal/observabilitycomposition ./cmd/bria`.
  Все шесть package targets проходят. Ключевые тесты: `TestFinalProcessorPersistsOneExactManualDecisionAcrossRestart`, `TestManualRetryIsExactOneShotAndUnknownRotatesDecision`, `TestReopenReconcilesInFlightClaimToNewManualGenerationWithoutSend`,
  `TestOpenReadsBoundedRetryKeyAndRoutesFinalIntoArtifactProduction`,
  `TestOpenFailsClosedForUnsafeRetryKeyWithoutCreatingArtifactState`,
  `TestOpenWiresP4FinalsAndPreparedObservability`,
  `TestOpenExplicitP4RuntimeRoutesOpaquePhotoCustodyToCodexRuntime`,
  `TestOpenP4RuntimeScreenGateUsesFileSettingsAndConfirmedTelegramReceipt`,
  `TestOpenP4RuntimeFailsClosedAtFirstVoiceForMissingOrSymlinkedModel`,
  `TestSubmitWithCallbacksPreservesCallbacksAndRecordsFirstTimings`,
  `TestSubmitWithCallbacksRecordsFailureAndCancellationWithoutChangingError`,
  `TestLoggingFailureAndInvalidScopeDoNotChangeSubmission` и
  `TestPreparedWrapperIsExplicitAndPlainSubmitDoesNotLog`.
- `internal/singlemachinecomposition/run.go` содержит 760 production lines при
  установленном architecture cap 800; `cmd/bria` target из команды выше
  проходит.
- `.gitignore` исключает `.avis/`, `bin/` и root production binaries; machine
  guard в repository policy отклоняет их tracking.
- `make check-policy` в этом снимке проходит policy, markdown links,
  filenames/secret scan. `make check-format` проходит.
- `make check-full` прошёл 2026-09-03 после двух test-only fixes: fixture с
  `{model_path}` для `containerpreflight` и deterministic timestamps в
  `authflow`. Это локальный gate текущего репозитория, не внешний release
  receipt.

### Только component-only

Следующие границы существуют и проверяются локально, но не образуют полный
внешний продуктовый acceptance:

- backup/restore composition;
- update composition и installer/rollback scripts;
- `internal/multinodecomposition`, `internal/nodebootstrap` и `internal/nodelink`
  для ручного coordinator cutover, pairing, TLS и durable replay;
- provider-native archive/discovery helpers.

### External-unverified или заблокировано

Не подтверждены внешним receipt или физическим пробегом:

- настоящий пользовательский Telegram flow с командами, callbacks, карточками,
  текстом, голосом и фотографией;
- реальный provider submit и authorization Codex/Claude с настоящими
  credentials;
- внешняя отправка artifact-файлов, включая Telegram receipt и retry;
- multi-computer coordinator/executor runtime и ручная передача роли;
- Linux, WSL и Docker full-path запуск с настоящими provider bundles и моделью;
- live Parakeet voice run;
- физические backup/restore и update/forced-rollback probes;
- production-equivalent observability/latency evidence.

Локальный `make check-full` и полный architecture checker на этом снимке
зелёные. Они не заменяют внешний acceptance: внешний Telegram/provider flow,
platform matrix, backup/update probes и production observability evidence
остаются непроверенными.

## Фактическая текущая композиция

### Рабочий single-machine путь

`cmd/bria` принимает только `help`, `version`, `run`, `check-config` и
`check-telegram`. `run` передаёт управление
`internal/singlemachinecomposition.Run`, который сейчас допускает version-0
combined-конфигурацию и versioned `combined` без сетевого listener.

Порядок композиции в `run`:

1. Строгая загрузка JSON, чтение ссылок на Telegram token и callback key,
   проверка Telegram `getMe`/username, затем открытие durable state.
2. `storage.SessionStore`, settings file store, provider config store,
   Telegram reply routes и notification part receipts.
3. `messagejournal` + `durableflow` для входных и исходящих записей, safe-log
   store и периодическая очистка журналов.
4. `runtimefactory` создаёт точные дочерние команды: соседний
   `bria-codex-adapter` или `bria-claude-adapter` запускает настроенный provider
   executable без shell; окружение очищается от Telegram token.
5. `sessionruntime`/`sessionsupervisor` запускают, останавливают и
   восстанавливают provider process; accepted-turn reconciliation использует
   explicit `runtime.discovery`, если он задан.
6. `app` предоставляет create/resume/close/stop/session lifetime и turn
   lifecycle; `interactioncomposition` и `authcomposition` подключают
   provider interactions и Telegram-facing API-key authorization.
7. `telegramcontroller` получает сценарии; `telegrambridge`, signed callback
   codec/registry, `telegramflow` и recovery executor проецируют карточки,
   callbacks и receipts в Bot API.
8. Параллельно работают session expiry, durable input/output dispatch,
   Telegram status delivery, outbound receipt reconciliation, supervision и
   safe-log cleanup.

### Opt-in и пока не полный runtime

- `turnruntimecomposition` теперь является controller-facing turn boundary:
  он собирает artifact final/retry, P4 media/Parakeet/Screen и передаёт
  `Finals` в P4; `singlemachinecomposition.Run` использует этот bundle.
- `artifactruntimecomposition` собирает final artifact delivery, durable
  manifest/retry и signed callback route; late publisher binding выполняется
  после создания Telegram flow.
- `backupcomposition` намеренно предоставляет только manual local operations;
  `updatecomposition` допускает manual trigger и opt-in schedule, но ни один
  из этих runtime не включён в основной цикл `run`.
- `observabilitycomposition` оборачивает provider submitter после P4
  capability selection и измеряет provider-accept/first-event/total; сырые
  prompt, result и file path не пишутся в safe log.
- Network runtime-пакеты существуют отдельно. `validateRunnableRole` сейчас
  fail-closed отклоняет `executor`, `coordinator` и combined с listener:
  сообщение - `network role runtime is not connected in this build`.

### Данные и границы владения

Координатор должен владеть Telegram, маршрутизацией, каталогом сессий и
недоставленными сообщениями; компьютер-исполнитель - реальной сессией
Codex/Claude, полной историей, рабочей папкой, media custody и локальной
авторизацией. В обычное глобальное состояние не входят полные стенограммы,
медиа, рабочие файлы, provider credentials, Telegram token и ключи связи.
Сессия сохраняет provider и computer на весь срок жизни; автоматический перенос
между компьютерами не допускается.

## Известные функциональные ограничения

### Архив и root mapping

Конфигурация допускает явные `runtime.discovery.codex_root` и
`runtime.discovery.claude_root`. Claude transcript store умеет читать
проверяемые JSONL summaries и accepted turns. Но startup `run` пока не
подключает полноценный discovery/import этих источников в единый session
catalog, а конкретный provider-native **Codex archive root mapping** для
платформ и компьютеров не зафиксирован рабочим runtime. Нельзя считать наличие
полей конфигурации доказательством найденного архива или точного resume.

Acceptance probe: на каждой целевой ОС задать известные roots с тестовыми
сессиями обоих providers, выполнить discovery, получить deterministic catalog,
проверить provider/session/workdir identity и exact resume; отсутствующий или
неоднозначный root должен дать явный отказ без изменения текущего catalog.

### Фото Claude

Photo custody подготавливает durable attachment, но
`providerinputcomposition` намеренно возвращает
`ErrProviderAttachmentsUnsupported` для Claude и не читает photo path.
Структурированные фотографии сейчас поддержаны только для Codex; для Claude
нет безопасного image handoff и нет разрешения молча превращать путь или bytes в
prompt text. Это должно остаться явным user-facing ограничением либо быть
изменено отдельным решением и внешним тестом.

## Нерешённые решения

До production нужно зафиксировать решения, а не выводить их из текущих
заглушек:

- **Auth:** оставить API key flow для обоих CLI или добавить subscription/OAuth
  flow. Сейчас реализована только API-key boundary: Codex через официальный
  CLI stdin, Claude через API-key `--bare`; OAuth/subscription login не
  поддержан.
- **Backup:** расписание создания и политика шифрования. Сейчас config требует
  `backup.schedule: null` и `backup.encryption: null`, а composition принимает
  только явно отключённый manual policy и не пишет owner storage.
- **Update:** расписание автоматической проверки/запуска. Сейчас interval `0`
  означает disabled; источник и trust key задаются конфигурацией, но физический
  update/rollback не принят.

Каждое решение должно иметь owner-visible запись, config migration/validation,
recovery procedure и acceptance probe.

## План до production

Порядок ниже - рабочий P0/P1/P2 backlog. Каждый пункт закрывается только
наблюдаемым acceptance probe, а не наличием функции или зелёным unit test.

### P0 - блокирует production

Локальный architecture/full-suite prerequisite закрыт: `make check-full`
прошёл 2026-09-03 после test-only fixes. Следующие пункты остаются внешними
или продуктовыми release blockers.

1. **Принять auth policy и пройти live auth.** Probe: на тестовом компьютере
   через Telegram авторизовать выбранные providers, проверить удаление secret
   message, отсутствие секрета в logs/state/backup и повторное включение без
   нового login. Если выбран OAuth/subscription, API-key-only implementation
   недостаточна.
2. **Подключить archive discovery и exact resume.** Закрыть Codex root mapping,
   startup merge и единый Codex/Claude catalog; отдельно проверить Claude
   transcript roots. Probe: известные test sessions видны одним archive,
   resume сохраняет exact provider session ID/workdir, конфликт не мутирует
   catalog.
3. **Подключить multi-computer runtime.** Реализовать coordinator/executor
   roles вокруг существующих node protocol boundaries и оставить manual
   cutover only while old coordinator is live. Probe: pairing, TLS, ordered
   input, offline event/outbox replay, no auto-election, reread of state after
   cutover.
4. **Пройти настоящий Telegram/provider/media/artifact сценарий.** Probe:
   создать и продолжить сессии Codex и Claude, отправить текст, voice/photo,
   проверить explicit Claude-photo behavior, обнаружить и доставить artifacts,
   повторить только unknown/failed parts по Telegram receipt.
5. **Пройти platform matrix.** Probe для macOS, Linux, WSL, Docker combined,
   Docker coordinator и Docker executor: clean bootstrap, real Telegram,
   Codex/Claude, Parakeet voice, persistent state, explicit workspace mount и
   отказ от неподключённой папки.
6. **Принять backup policy и доказать restore.** Probe: одна последняя
   проверенная копия, повреждённый candidate не портит предыдущую, restore
   возвращает разрешённое состояние и не содержит secrets/media/files/logs/
   binaries/caches.
7. **Принять update schedule и доказать forced rollback.** Probe: подписанная
   версия обновляет executors по одному, coordinator последним; broken release
   останавливает rollout и возвращает предыдущую рабочую версию с тем же
   state/queue/session identity.

### P1 - эксплуатационная готовность

- Наблюдаемость: correlation IDs, bounded diagnostics, provider accept/first
  event/total, external receipts и алерты без секретов.
- Повторяемые crash/restart probes для process, adapter, coordinator, network
  link и контейнера с перечитыванием durable state.
- Документированные установки/autostart для macOS/Linux/WSL и immutable image
  update path для Docker.
- Проверка cleanup TTL журналов 6/24/72 часа на реально работающем процессе.
- Проверка отказов повреждённых config, callback key, provider lock, release и
  backup без повреждения последнего good state.

### P2 - после первого production gate

- Улучшение пользовательских диагностик и recovery projections.
- Расширение provider-native discovery после закрытия Codex/Claude baseline.
- Поддержка Claude images только после отдельного security/provider decision.
- Дополнительные performance/latency baselines и capacity limits для больших
  histories, attachment batches и concurrent sessions.

## Offline bootstrap и проверки

Команды ниже не требуют Telegram, Codex, Claude, Parakeet или сети. Они
используют только локальные зависимости, уже доступные в checkout. Значения в
временных secret files - фиктивные тестовые строки, не реальные credentials.

```sh
set -eu
tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT
umask 077
printf '%s\n' 'offline-token-placeholder' > "$tmp_dir/telegram-token"
printf '%s\n' '0123456789abcdef0123456789abcdef' > "$tmp_dir/callback.key"
cat > "$tmp_dir/config.json" <<EOF
{
  "owner_user_id": 1,
  "private_chat_id": 1,
  "bot_username": "offlinebot",
  "state_path": "$tmp_dir/state",
  "telegram_token": {"secret_file": "$tmp_dir/telegram-token"},
  "callback_key": {"secret_file": "$tmp_dir/callback.key"},
  "providers": {
    "codex": {"enabled": false},
    "claude": {"enabled": false}
  }
}
EOF

make check-policy
make check-format
make build
bin/bria --help
bin/bria version
bin/bria check-config --config "$tmp_dir/config.json"
```

`check-config` - offline read-only composition/preflight относительно runtime
и secret file references; он не обращается к Telegram и не захватывает lock.
`check-telegram` - отдельный network-only `getMe` probe. `run` не является
offline check: он захватывает lock, создаёт/перечитывает durable state и
подключается к Telegram.

Offline bootstrap на этом снимке проходит `make build`, `bin/bria --help`,
`bin/bria version` и `bin/bria check-config` с временной конфигурацией.
Это не заменяет race, packaging и фактический внешний запуск.

Для повторной локальной проверки полного набора:

```sh
make check-full
```

### Config contract для Parakeet

`parakeet.argv` - прямой `exec` без shell и должен содержать ровно один
placeholder `{model_path}`. `executable` и `model_path` - проверяемые
абсолютные пути; Bria подставляет значение `model_path` перед запуском.

```json
{
  "parakeet": {
    "executable": "/absolute/path/to/parakeet-wrapper",
    "model_path": "/absolute/path/to/parakeet-model.bin",
    "argv": ["--model", "{model_path}"]
  }
}
```

Wrapper/model должны быть подготовлены оператором и, в Docker, подключены
явными read-only mounts. Placeholder нельзя заменять shell expansion или
передавать через неподписанный launcher.

## Первое продолжение на другом устройстве

1. Перенести текущий checkout целиком и проверить `git status`, branch и
   наличие всех untracked files до первого commit.
2. Прочитать этот handoff, затем [архитектуру](ARCHITECTURE.md),
   [решения](DECISIONS.md), [платформы](PLATFORMS_AND_DEPLOYMENT.md) и
   [приёмку](TESTING_AND_ACCEPTANCE.md).
3. Не считать отсутствие remote, отсутствие commit, созданный binary или
   локальный passing component test доказательством production.
4. Сначала зафиксировать принятые auth/backup/update decisions, затем
   запускать дорогие внешние probes по оставшимся P0.

На момент снимка `main` не содержит коммитов, remote не настроен, GitHub auth
подтверждён для аккаунта `Time4Mind`, а рабочее дерево показывает
новый репозиторий как набор untracked файлов. Артём выбрал
целью публичный новый repository
[`Time4Mind/bria`](https://github.com/Time4Mind/bria).
Read-only probe этого адреса
[`Time4Mind/bria`](https://github.com/Time4Mind/bria) разрешается в публичный
canonical `Time4Mind/bria-legacy`; это не текущий checkout, не источник
доказательств и не цель работы. При создании нового `Time4Mind/bria`
перенаправление старого имени прекратится; сам `Time4Mind/bria-legacy`
нельзя изменять.
Следующий владелец должен сначала создать первый согласованный Git handoff
commit после проверки содержимого и секретного скана; публикация remote в этот
документ не предполагается.

Deployment postflight 2026-09-09: commit `817888e` was installed as
`/Users/a-s-nosko/.local/opt/bria-v2/releases/20260909-persistent-terminal-final`
and only `gui/501/com.time4mind.bria.v2` was restarted. New PID `4795` is
stable (`runs=1`, `last exit code=never exited`), lock ownership is correct,
`check-state`, config and Telegram identity probes pass, and two snapshots three
seconds apart are identical. The pre-existing legacy session without a saved
terminal attach manifest remains in `awaiting_recovery`; it cannot be safely
adopted after the old binary was stopped. No session input was replayed.

## Current release checkpoint 2026-09-09

После одобрения полного объёма A31+A32 реализованы persistent terminal attach/
detach, accepted-turn recovery без повторной отправки, strict binding fence,
canonical final для root/steer, локальный retry финализации и bounded projection
для oversized tool records. Полный `make check-full` пройден: policy, links,
secret scan, format, architecture, unit tests, vet, operational packaging,
race tests и executable-trio acceptance. Отдельный flaky processgroup race
тест дополнительно прошёл 3/3 раза.

Рабочее дерево готово к согласованному Git release. Live service пока не
перезапущен: перед switch нужно сохранить текущий snapshot и закрыть
проверку первого handoff старого процесса с общей PGID. Runtime state,
credentials, user sessions и внешний input не менялись.
