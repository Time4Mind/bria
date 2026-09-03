# Handoff: статус и следующий план

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
