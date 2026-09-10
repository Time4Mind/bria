# A39: создание сессии и recovery без тупиков

## Договор

- Результат: диагностировать задержку последнего `Новое`, незавершившееся
  создание и каждый доступный эпизод `awaiting_recovery`.
- Источники: текущий код, `state.json`, безопасные structured logs и read-only
  process metadata новой Bria на 2026-09-10, Europe/Moscow.
- Population: последняя попытка создания и все текущие либо доступные в
  ротированных логах recovery-эпизоды. Legacy и сырые payload исключены.
- Writes: по явному указанию Артёма разрешены product/tests/docs текущего repo,
  затем standing release sequence для Time4Mind/bria и существующего сервиса
  `gui/501/com.time4mind.bria.v2`. State/config/secrets и исходящие сообщения
  вручную не изменяются.
- Acceptance: UTC+MSK таймлайн, первый неверный переход, причины либо явно
  названная недостающая телеметрия, отдельный статус каждого обязательства.

## Todo и evidence

| ID | Обязательство | Статус | Evidence / открытая граница |
|---|---|---|---|
| A39-1 | Локализовать задержку `Новое` | diagnosed | 07:41:54.186-07:41:54.852 UTC: ingress 202 ms, Telegram edit 161 ms, durable delivery 389 ms; click-to-ingress в текущих логах не измеряется |
| A39-2 | Проверить факт создания | diagnosed | Session `ab396f5b...` сохранена 07:42:02 UTC и стала `awaiting_recovery` 07:42:08 после `adapter_failed` за 6152 ms; binding отсутствует, active вернулся к прежней session |
| A39-3 | Разобрать текущий recovery-кейс | diagnosed | Единственная текущая awaiting session имеет `recovery_target=starting`, `binding=null`; sweep её не выбирает и retry не запускает |
| A39-4 | Разобрать доступную историю recovery | diagnosed | 682 `session.recovery_failed`: одиночные 08.09 13:41 UTC и 09.09 07:08 UTC; затем 680 записей 09.09 09:56-10:52 UTC, почти всегда 2 ошибки каждые 10 s. Session IDs в critical log отсутствуют |
| A39-5 | Исправить безопасную startup-диагностику | verified_focused | Allowlisted stage/class, exact logical session ID и per-attempt events без raw stderr; staged marker сохраняется даже после ранней safe-class строки |
| A39-6 | Исключить тупик initial start | verified_focused | 3 попытки одной durable identity; persisted `starting` нормализуется без запуска, retained non-empty unbound session пропускает generic startup и автоматически восстанавливается live через config-aware starter; cancellation после binding ограждена bounded finalization context |
| A39-7 | Остановить бесконечный recovery loop | verified_focused | Автоматический per-session/per-lifecycle backoff 1/2/4/8/16/20 min, далее 20 min; reset при смене binding/lifecycle или успехе; одна correlated запись на фактическую ошибку |
| A39-8 | Проверить Telegram active/list после failure | verified_focused | Public regressions: starting видна сразу; retained awaiting остаётся активной до auto-recovery; доказанно пустая удаляется; поздний failure не перезаписывает новый выбор пользователя |
| A39-9 | Полный выпуск и live postflight | complete | Runtime `1240d77`; оба exact-SHA CI PASS; `20260910-session-recovery` установлен, PID 20745 running/sole lock; hashes, config/settings и population сохранены; `ab396f5b...` автоматически recovered в Ready с binding |
| A39-10 | Сохранить architecture boundaries | verified | Поведение вынесено в `initialstartretry`, `nativestartupdiagnostic`, `recoverybackoff`; новые edges и минимальные budgets зарегистрированы; `make check-architecture` PASS |
| A39-11 | Устранить обнаруженную full-gate PID race | verified_focused | `sessionruntime` helper публиковал PID через truncate/write, reader видел пустой файл; заменено на temp+rename для трёх PID receipts, regression 100/100 PASS |

## Карта покрытия

- App lifecycle: `internal/app/create_session*` и новый retry package; owner -
  отдельный исполнитель; результат - retry и доказанное удаление failed start.
- Recovery scheduler: `internal/supervisioncomposition/*` и новый backoff package;
  owner - отдельный исполнитель; результат - отсутствие 10-second retry storm.
- Native startup classification: `internal/nativeadapter/*`, новый diagnostic
  package и adapter commands; owner - отдельный исполнитель; результат - safe
  stage error classes и cleanup.
- Integration/UI/composition/docs/release: integration owner; зависимости - три
  зоны выше. Полнота определяется общими public tests и live postflight, не
  отчётами исполнителей.

## Проверенные выводы

1. Последний обработанный `Новое` внутри Bria занял менее секунды; наблюдаемые
   3-5 секунд не находятся между ingress и Telegram edit.
2. Wizard не потерял intent: он создал durable session и карточку, но startup
   завершился до provider binding. Пользовательски это выглядит как отсутствие
   новой рабочей сессии.
3. `awaiting_recovery + recovery_target=starting + binding=null` сейчас является
   тупиком: live sweep выбирает только binding-bearing sessions, а manual/startup
   recovery также требует prior binding.
4. Исторический шум - реальные повторные попытки, а не дубли одного события:
   339 sweep-моментов, почти всегда по две ошибки каждые 10 секунд.
5. Startup не должен запускать unbound initial session: он сохраняет её для
   live manager, где первая попытка немедленная, а следующие входят в единый
   backoff и получают session-correlated safe log.

## Не доказано

- Низкоуровневая причина текущего `adapter_failed`: allowlisted лог сохраняет
  только общий класс и не пишет stage. Процесс, созданный в 10:42 MSK, к моменту
  проверки отсутствовал; Bria не перезапускалась.
- Интервал между физическим нажатием кнопки в Telegram и `ingress.received`:
  callback update не содержит измеряемого client-click timestamp.

## Release receipt

Runtime commit `1240d771ce34740cab059d15f7223ca8a8a626c2` находится в
`origin/main`. Полный локальный
`VERSION=20260910-session-recovery make check-full` прошёл. Exact-SHA Stage 1
run `34463753241` и Platform run `34463753299` завершились успешно, включая
полный race suite и native macOS/Ubuntu checks.

Версия `20260910-session-recovery` установлена; `previous` указывает на
`20260910-ci-acceleration`. Хэши установленной тройки: Bria
`b1866c92...41575`, Codex adapter `1e293178...f7eb`, Claude adapter
`8c4ce502...aa92`. После restart `gui/501/com.time4mind.bria.v2` работает с
PID 20745, и только этот PID держит `.state.json.lock`; config check и Telegram
identity прошли.

Postflight 2026-09-10 13:07 MSK: сохранены 8 сессий, 8 карточек, 555 записей
истории, 13 journal sessions и 27 inputs; config/settings hashes не изменились.
Проблемная `ab396f5b...` без ручного вмешательства перешла из unbound
`awaiting_recovery` в `ready` с binding. После `telegram.flow_ready` свежих
critical events нет. Новый пользовательский Telegram input не отправлялся;
интервал client-click -> ingress по-прежнему не измеряется.
