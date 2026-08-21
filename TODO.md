# Bria TODO

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
- [ ] P11. Улучшить диагностический контракт логов: timestamp, PID, version и
  commit в каждой структурной записи; failure class; связь ingress с operation;
  корректный severity для повторённого callback; метрики RSS, goroutine, FD и
  дочерних процессов.
- [ ] P12. Сохранить provenance развертываний. Startup должен печатать точные
  version/commit и уникальный binary identity; история не должна зависеть от
  перезаписываемого `current`-пути. В аудите промежуточный commit работал 97 с,
  но восстановить его binary после следующей установки было невозможно.
