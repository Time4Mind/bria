# A50: performance and resource efficiency audit

## Договор задачи

- Результат: измерить эффективность текущей Bria в простое и при обычной
  работе, найти источники лишних CPU/RSS/wakeups/I/O и задержек, самостоятельно
  исправить очевидные локальные причины без изменения продуктовой семантики.
- Источники: установленная `20260911-voice-carrier-latency` на commit
  `e372c21862d714da4d27a404c3c35310b17df157`, launchd/process evidence,
  безопасные service traces, профили и текущий исходный код.
- Период и timezone: live-измерения 2026-09-12, Europe/Moscow; исторические
  записи используются только для выбора гипотез и не доказывают текущее
  состояние.
- Grain/population: основной процесс Bria и принадлежащие ему фоновые циклы;
  дочерние Codex/Claude/tmux процессы учитываются отдельно, чтобы не приписать
  их стоимость Bria. Состояния: устойчивый простой, пассивное обновление и
  контролируемый synthetic work без исходящих Telegram-сообщений.
- Exclusions: не менять config/state/secrets, не создавать пользовательские
  Telegram inputs, не останавливать живые provider-сессии ради замера. Большие
  изменения UX, lifecycle или polling policy сначала согласовать.
- Writes: product code, tests и docs этого репозитория; после полной приёмки
  действует standing release в `origin/main` и restart только
  `gui/501/com.time4mind.bria.v2`.
- Приёмка: repeatable before/after probes показывают снижение измеренного
  расхода или устраняют доказанную лишнюю работу; пользовательские latency и
  correctness regressions отсутствуют; focused/race/full gate, exact-SHA CI,
  install/restart и safe live postflight GREEN.

## Coverage map

| ID | Зона | Owner и границы | Stop criterion | Статус |
|---|---|---|---|---|
| A50.1 | Live idle/work baseline | read-only agent; launchd/process/log evidence | CPU/RSS/threads/FDs и stacks измерены отдельно для Bria и children | verified before/after |
| A50.2 | Timers, polling, retries, I/O | read-only agent; source inventory | все recurring loops ранжированы по частоте и стоимости, названы простые кандидаты | verified |
| A50.3 | Allocation/concurrency seams | read-only agent; tests/benchmarks/architecture | найдены измеримые hot paths и безопасные regression seams | verified |
| A50.4 | Synthesis and simple fixes | integration owner; непересекающиеся файлы назначаются после RCA | причина доказана before/after, концептуальные развилки отделены | local verified |
| A50.R | Release | integration owner | full gate, exact-SHA CI, verified install и postflight | verified |

## Гипотезы первого прохода

1. Частые ticker/poll loops просыпаются даже без изменившегося состояния.
2. Telegram/session projection повторно читает или сериализует полный state.
3. Background recovery/preprocessing supervisors выполняют лишние scans при
   устойчивом Ready.
4. Основной RSS создают provider children, а не Bria; оптимизация parent тогда
   не должна маскировать чужую стоимость.

## Baseline до исправлений

Замер выполнен на установленном commit `e372c218` в устойчивом простое
00:07:40-00:20:19 МСК. После 23:56:35 МСК не было пользовательских или
модельных событий; journal содержал 3 343 outputs и ноль pending. Независимый
26-секундный замер parent дал `12.14 / 26 = 46.7%` одного ядра.

| Объект | CPU/RSS | Проверенное объяснение |
|---|---:|---|
| Bria parent | `45.9-47.6% CPU`, `79.2 MiB RSS`, `559-588 wakeups/s` | горячий stack декодирует весь message journal в output sweep |
| Пять native adapters | `8.9% CPU`, `76.3 MiB RSS`, около `2 708 wakeups/s` | каждый каждые `150 ms` проверяет tmux и экран |
| Пять detached tmux servers | `1.3% CPU`, `27.1 MiB RSS` | отдельные владельцы provider process trees |
| Provider processes под tmux | около `0.02% CPU`, `1 726 MiB RSS` | память принадлежит Codex/Node/CUA; суммарный RSS завышен shared pages |

`vmmap` основного процесса показал physical footprint `39.6-47.9 MiB`, peak
`65.3 MiB`, 22 threads и стабильные 30 FD. Признаков краткосрочной утечки не
найдено.
Message journal имеет размер `1,449,273` bytes, state - `2,637,473` bytes.

Рабочий режим оценён по safe timing logs за 2026-09-11 UTC. Для 76 входящих
операций durable delivery имел median `701 ms`, p95 `1352 ms`; Telegram edit
для 340 проекций - median `708 ms`, p95 `1944 ms`; локальный state commit -
median `112 ms`, p95 `214 ms`. Это end-to-end этапы с сетевым Telegram I/O,
поэтому они не равны чистому CPU Bria. Codex accept для 49 запросов имел median
`1046 ms`, p95 `5871 ms`; model execution учтён отдельно и не приписан Bria.

## Доказанные причины

1. Idle output dispatcher каждые `500 ms` читал state один раз, затем полностью
   декодировал message journal отдельно для каждой из 20+ сессий.
2. Native adapter опрашивает CLI каждые `150 ms`. Дополнительно неизменный
   screen capture обходил собственный `300 ms` throttle из-за не обновлённого
   времени последнего наблюдения.
3. Каждая journal mutation читает, полностью записывает и перечитывает файл;
   для текущих `1.45 MiB` это около `4.35 MiB` перемещённых данных на mutation.
4. State/history и safe logs также используют полную сериализацию. Это заметно
   при работе, но изменение persistence policy уже является продуктовым
   компромиссом, а не локальным исправлением.

## Реализованные локальные исправления

1. Периодический output sweep теперь ищет следующий output для всех сессий в
   одном snapshot журнала. Wake конкретной сессии и durable ordering сохранены.
   Regression seam: 20 idle sessions, startup плюс один sweep - `40 -> 2`
   journal reads, снижение на `95%`. Failed/Unknown или transport error одной
   сессии исключает только её из текущего sweep и не задерживает готовый output
   другой сессии; два RED/GREEN tests фиксируют эту межсессионную изоляцию.
2. Неизменное native observation теперь продвигает timestamp throttle. При
   стабильном экране capture cadence снижается примерно с `6.67` до `3.33`
   попыток в секунду на adapter без изменения emitted events.
3. Focused tests, race tests изменённых пакетов и полный
   `VERSION=20260912-performance-efficiency make check-full` GREEN. Для трёх
   существующих responsibilities после уплотнения API package budgets подняты
   стандартными шагами по 25 строк: `550 -> 575`, `700 -> 725` и
   `1700 -> 1725`; machine checks обновлены вместе.
   Live after-замер и release зафиксированы ниже в A50.R.

## Отложенные решения

Артём отложил эти вопросы 2026-09-12. Они сохранены в контексте A50, не
блокируют завершённый release и не разрешают менять продукт без нового
возврата к соответствующему ID.

| ID | Контекст и вопрос | Компромисс, который нужно согласовать |
|---|---|---|
| A50.D1 | Output dispatcher: переводить ли периодический sweep на событие с recovery-проверкой раз в `5 s`? | В штатном потоке обновление остаётся немедленным; потерянное wake-событие может быть обнаружено на `0-5 s` позже. |
| A50.D2 | Native CLI: включать ли адаптивный poll `150 ms` во время активности и `1 s` в простое? | Снижается idle CPU адаптеров; закрытие терминала в простое может определиться на срок до `850 ms` позже. |
| A50.D3 | Journal: какой объём подтверждённой истории сохранять при compaction? | Меньший retention ускоряет рабочие mutation, но сокращает доступную историю для recovery и rollback. Нужен точный срок или число записей. |
| A50.D4 | Safe logs: разрешать ли batching записей на `100-250 ms`? | Меньше `fsync`; при hard crash может не сохраниться последний короткий интервал подробной телеметрии. |
| A50.D5 | Directory activity: считать ли её только при открытии browser вместо рекурсивного scan каждые две минуты? | Меньше фонового I/O; сортировка может быть неактуальной до следующего открытия browser. |

## Live after и первый release receipt

Source commit `62b5a47b4cde7108d7783bdecb38f1f4306da1ba` прошёл Stage 1 run
`34650980034` и Platform build matrix `34650980010`, оба `success`. Signed
release `20260912-performance-efficiency` установлен, postflight GREEN; service
перешёл `runs 15 -> 16`, PID `80188 -> 99061`.

После стабилизации пять последовательных ненулевых точек parent CPU дали
`12.2/13.8/12.7/13.7/17.0%`, среднее `13.9%`. От baseline `46.7%` это снижение
на `70.3%`. Parent RSS после restart был `45-47 MiB`; это не выдаётся за
доказанное memory-улучшение, поскольку чистый restart сам меняет resident set.

Для пяти native adapters три after-среза дали суммарно `5.6/6.4/6.3% CPU`,
среднее `6.1%`. От baseline `8.9%` это снижение на `31.5%`. Оставшаяся нагрузка
соответствует неизменённому `150 ms` tmux liveness polling и вынесена в решение
Артёма.

Message journal, settings и preprocessing satellite hashes до/после restart
совпали. Основной state ожидаемо изменился при recovery projections. После
старта нет service/critical errors; шесть bounded `navigation_suppressed` с
категорией `cancelled` относятся к отмене устаревших startup projections и не
повторяются. Старые строки `unknown callback recovery identity` в `bria.log`
не являются текущей ошибкой: файл не менялся с 2026-09-11 19:23:16 МСК.

Рабочая CPU-нагрузка live не измерялась: в окно after-профиля не было
естественного пользовательского запроса, а synthetic Telegram input исключён
договором. Исторические safe timings и component regression seams приведены
выше; новый live work sample остаётся наблюдением, а не блокером выпуска.

Финальный commit `7f741e4bd78ea4dc2f4e35614b0d3159e01233b6` прошёл Stage 1
`34651941097` и Platform build matrix `34651941178`, оба `success`. Signed
release `20260912-performance-efficiency-v2` установлен; process/lock,
артефактные hashes и safe postflight проверены на запущенном сервисе
`gui/501/com.time4mind.bria.v2`.
