# Bria TODO

## Следующий рефакторинг

Порядок обязателен: сначала три изолированных исправления ниже, затем отдельный
аудит и изменение границ packages. Каждый пункт выполняется отдельным commit-ом
с package/race-тестами, установкой, перезапуском и runtime-проверкой.

- [x] R1. Ограничить и периодически очищать process-local состояние сессий,
  прежде всего `sessionPages` и `promptHashes`. Очистка должна учитывать active,
  archived и удалённые сессии и не менять пользовательский flow.
- [x] R2. Свести изменение response-card к одному per-user coordinator вместо
  распределённого управления `cardEditMu`/`cardMutationMu`. Не держать state
  lock во время Telegram I/O; сохранить epoch/generation guards и режимы
  response-card.
- [x] R3. Разделить крупные orchestrator-файлы по самостоятельным обязанностям
  внутри существующих packages. Не создавать новый package только из-за числа
  строк; сначала сохранить текущее направление зависимостей и поведение.
- [ ] R4. После R1-R3 провести аудит компоновки Go packages по официальным
  рекомендациям Go отдельными изолированными изменениями:
  - [x] R4.1. Перенести Telegram-проектор и его визуальные контракты из
    нейтрального `application` в `telegramapp`. Защитить границу architecture-
    тестом, запрещающим production-импорты Telegram из `application`.
  - [x] R4.2. Проверить связность обязанностей и fan-out `telegramapp`; отделять
    только самостоятельные ответственности, не создавая формальных
    micro-packages ради размера файлов или пакета:
    - [x] R4.2.1. Выделить чистую проекцию actor-authorized state и callback-
      tokens в `internal/telegramview`. Оставить Bot API transport, update-
      routing и background lifecycle в `internal/telegramapp`; запретить
      обратную зависимость architecture-тестом.
    - [x] R4.2.2. Выделить transport-level outbound coordination в
      `internal/telegramoutbound`: сериализацию Bot API writes, flood-wait,
      latest-message ordering и timing. Replicated response-card state,
      visible epoch, active-session guards и page watermarks оставить единым
      semantic coordinator в `telegramapp`.
  - [x] R4.3. Проверить, что межпакетные interfaces узкие, принадлежат
    потребителю и не создают церемониальных слоёв:
    - [x] R4.3.1. Принимать cluster updater в `telegramapp` через локальный
      трёхметодный consumer-owned interface, а не через конкретный
      `*clusterupdate.Coordinator`.
    - [x] R4.3.2. Разложить широкий `SessionControls` на consumer-owned порты
      ввода, lifecycle, интерактива, pane capture, транскрипта и файлов. В
      composition root сохранить один составной контракт и один controller.
  - [ ] R4.4. Зафиксировать проверяемое направление зависимостей и отсутствие
    циклов и пакетов вида `util`/`common`/`api`/`types`/`interfaces`.
  - [ ] R4.5. Подтвердить итоговую компоновку: один `go.mod`, `cmd/bria` как
    composition root, production-код в `internal`; актуализировать
    архитектурную документацию по фактическому коду.

Зафиксировано по аудиту production-логов за три последних развернутых commit-а
на 2026-08-21. Пункты 1-4 выполняются отдельно и поэтому сюда не включены.

## Обнаруженные артефакты

- [x] P5. Снижен фоновый polling и объём detail-логов. Active live-card worker
  остаётся на прежнем интервале 1,5-2,5 с, но исключает дублирующее чтение из
  общего цикла; фоновые running-сессии проверяются fallback-сканом раз в 5 с,
  а ошибки повторяются через 1,2 с. Stop/StopFailure по-прежнему ускоряет final,
  а `transcript_trigger_gap` фиксирует только завершения, найденные watchdog и
  не подтверждённые событийным trigger за grace-период. Быстрые неизменившиеся
  cache-hit/card timing больше не создают detail-строки.
- [x] P6. Разделена политика завершения без matching response-card. Фоновая
  сессия получает `background_settled`: final остаётся в transcript и полная
  карточка появляется только после явного выбора. Известный menu/create flow
  не вытесняется (`not_visible`). Для действительно активной сессии потерянная
  registry-карточка восстанавливается один раз с повторной проверкой active ref,
  runtime generation, provider binding и visible epoch; stale replacement
  удаляется. Runtime generation протянута в provider Stop как optional поле с
  legacy-совместимостью.
- [x] P7. `provider_stop` ограничен единым deadline 3 с. Сигналы одного turn
  объединяются в один bounded flight, новый turn отменяет старый, а retry
  разрешён только для явного списка временных исходов. Итоговая строка содержит
  attempts, число дублей и причину handoff в 5-секундный watchdog.
- [x] P8. Restore получил единый коррелируемый `restore_timing` по
  `ref+generation`: callback/ACK, Raft restore/select, ожидание heartbeat,
  archive verify, tmux resume/register, recovery commit и settled card. Медленные
  render/outbound строки помечаются restore stage; отдельно измеряется ожидание
  card-edit lock, transcript/pane, очередь и Telegram transport. Failed и
  superseded generation не маркируются как ready.
- [x] P9. Host lifecycle input получил единый `input_timing` по
  `ref+generation+operation`: durable store/FIFO, индивидуальное ожидание
  attachment, resolver/download/transcribe/prepare и tmux send. Duplicate
  submit не дублирует метрику, abandoned input получает terminal outcome;
  содержимое, file identity и пути не логируются.
- [x] P10. Stale create-flow закрыт постоянными regression probes для текста и
  голоса: после выбора существующей сессии input ровно один раз поступает в
  exact active ref и обязательно создаёт видимый ответ, а не завершается молча.
- [x] P11. Структурные записи получили единый bounded envelope с UTC timestamp,
  PID, version, commit, severity и явным failure class; raw Raft/subprocess
  streams оставлены отдельным совместимым каналом. Введена независимая от
  мессенджера корреляция `interaction ingress -> operation`, при этом внешний
  ingress ID хешируется и содержимое запросов не логируется. Повтор callback
  имеет Service/Detail severity, а terminal Critical и уведомление возникают
  только после durable cursor commit. Раз в 5 минут один последовательный
  sampler пишет current RSS, goroutine, FD и прямых дочерних процессов с cap,
  cancellation и partial availability; процессы внутри tmux не выдаются за
  прямых потомков Bria.
- [x] P12. Provenance развертываний хранится независимо от `current`: локальная
  macOS-установка создаёт immutable `releases/<binary-sha256>` с `release.json`,
  атомарно переключает `current` и сохраняет `previous`; legacy in-place
  directory переносится целиком как первый rollback target. `version` и startup
  service record содержат точные version, commit, build time и SHA-256
  фактического executable. Cleanup защищает active, previous, running и pending
  releases, а также сохраняет ещё два последних executable artifacts.
- [x] P13. Архив на каждой странице показывает до шести сессий в одном формате:
  непрерывный номер, имя, две короткие строки описания и разделитель. Текстовый
  номер страницы и общее количество убраны; пагинация остаётся в кнопках.
  Описание один раз выводится дешёвой моделью метаданных из первых 1-3 запросов,
  хранится в Raft отдельно от исходных prompt-ов и сбрасывается после restore.
- [x] P14. Разделитель каждой архивной сессии вынесен в отдельный визуальный
  блок: между описанием и линией, а также после линии остаётся по одному
  отступу. Модель и domain validation ограничивают две строки описания суммарно
  шестнадцатью словами.
