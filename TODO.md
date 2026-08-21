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
- [ ] P7. Ограничить retry `provider_stop`: один сигнал занял 5,566 с и три
  попытки, после чего был признан устаревшим (`outcome=ignored`). Нужны общий
  deadline, дедупликация по turn и явная причина остановки retry.
- [ ] P8. Разобрать задержку callback восстановления сессии 1,421 с. Само
  восстановление завершилось успешно; нужен phase timing, чтобы отделить
  transcript, Raft apply, tmux resume и Telegram edit.
- [ ] P9. Разобрать задержку доставки voice input 8,766 с. Запрос не потерян и
  был доставлен один раз; нужно разнести speech recognition, host FIFO,
  attachment wait и tmux send по отдельным метрикам.
- [ ] P10. Оставить regression probe для stale create-flow. Исправлено в
  `eafbfb0`, но нужен постоянный тест: голосовое или текст после устаревшего
  flow обязаны перейти в активную сессию, а не завершиться молча.
- [ ] P11. Улучшить диагностический контракт логов: timestamp, PID, version и
  commit в каждой структурной записи; failure class; связь ingress с operation;
  корректный severity для повторённого callback; метрики RSS, goroutine, FD и
  дочерних процессов.
- [ ] P12. Сохранить provenance развертываний. Startup должен печатать точные
  version/commit и уникальный binary identity; история не должна зависеть от
  перезаписываемого `current`-пути. В аудите промежуточный commit работал 97 с,
  но восстановить его binary после следующей установки было невозможно.
