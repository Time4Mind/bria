# A41 - постоянный preprocessing-сателлит каждой сессии

## Договор

- Результат: настройка `satellite preprocessing` имеет режимы `disabled`,
  `shared` и `per_session`. `shared` держит один постоянный satellite для всех
  основных сессий и обслуживает их FIFO; `per_session` держит один постоянный
  satellite на каждую основную сессию.
- Grain: одна связь `primary session ID -> satellite lifecycle`; satellite
  принимает все preprocessing-запросы только своей основной сессии и сохраняет
  контекст между ними.
- Lifecycle `per_session`: close основной сессии завершает provider-процесс и
  переводит основную сессию и satellite в archive; restore восстанавливает оба.
  Satellite отдельно не отображается в Archive и других пользовательских
  списках. Lifecycle `shared`: archive/restore/close отдельных основных сессий
  не закрывают общий satellite; он живёт до shutdown Bria.
- Exclusions: при `disabled` satellites не создаются и не используются.
  One-request-per-satellite больше не является целевым поведением.
- Default/migration: значение по умолчанию - `shared`; существующее
  `preprocessing_enabled=true` мигрирует в `shared`, `false` - в `disabled`.
- Отдельный RCA: определить причину последнего падения live Bria по safe logs,
  launchd/process evidence за текущий release window, timezone Europe/Moscow;
  не выводить сырые пользовательские payload.
- Разрешённые writes: код, тесты и документация текущего репозитория. После
  полного результата действует standing release authorization из `AGENTS.md`.
- Acceptance: RED/GREEN public lifecycle tests, race и полный gate, exact-SHA
  CI, live service postflight с сохранностью state/settings/journal.

## Coverage map

| Зона | Owner | Результат | Stop criterion |
|---|---|---|---|
| Lifecycle/persistence | implementation owner | стабильная связь primary-satellite | create/archive/restore/close tests |
| Routing/preprocessing | implementation owner | запросы не смешиваются между sessions | multi-session/context tests |
| UI invisibility/settings | review owner | satellite не попадает в lists/cards/archive | public view/settings tests |
| Crash RCA/release | integration owner | доказанная причина и безопасный postflight | bounded logs + live process evidence |

## Todo

| ID | Обязательство | Acceptance probe | Статус | Evidence / граница |
|---|---|---|---|---|
| A41.1 | Постоянный shared satellite и FIFO | prompts разных sessions используют один thread последовательно | verified locally | Unit/race: один protocol owner до durable Accept. Live Luna 2026-09-10: два разных primary прошли через один thread; после остановки adapter новый process exact-resume-ил тот же thread ID. Default/migration - shared. |
| A41.2 | Постоянный per-session satellite и изоляция | повторные prompts сохраняют thread, разные sessions не смешиваются | verified locally | Race-тесты подтверждают постоянный binding внутри primary, независимые параллельные lanes разных primary и FIFO только внутри своей lane. |
| A41.3 | Archive/restore/close lifecycle | shared не закрывается; personal архивируется и восстанавливается вместе с primary | verified locally | Close - операция, archive - durable итог. Binding сохраняется, provider process закрывается после main archive, exact restore использует тот же thread. Main completion публикуется до bounded satellite cleanup. |
| A41.4 | Satellite скрыт из UI/archive | public projection tests | verified locally | Сателлит хранится только в отдельном private binding store и не создаёт `domain.Session`; Telegram transport/UI содержит только переключатель режима. |
| A41.5 | Setting gates весь lifecycle | enabled/disabled transition tests | verified locally | Settings v6, strict round-trip трёх режимов, legacy bool migration, signed callback transport и controller reconfigure race PASS. Mode-marker сохраняет rollback-compatible v1 envelope и инструкцию 16 KiB. |
| A41.6 | RCA последнего падения Bria | launchd + safe log correlation | verified | Обычный message update 783531724 был durably queued, затем legacy keyboard projection была отклонена runtime; coordinator ошибочно запускал callback recovery для message и падал. Hotfix eeb4bb5 прошёл full gate/оба exact-SHA CI и live postflight: checkpoint 783531727, PID 88039, runs=1 |
| A41.7 | Полный выпуск и postflight | full gate, CI, hashes, service health | verified live | Source `b573960` в `origin/main`; полный post-review gate, Stage 1 `34511754264` и Platform `34511753964` PASS. Установлен `20260910-persistent-preprocess-satellites`: hashes трёх бинарников совпали, PID 47217 running/sole lock, config/state/Telegram identity OK, сохранены 8 sessions и 31 journal input. Config/settings/plist hashes не изменились. Один shared Luna satellite стал ready за 1205 ms. |
| A41.8 | Повторяющийся recovery failure без падения процесса | bounded RCA и regression probe | verified live | Семь отдельных `session.recovery_failed` для ab396f5b... в 19:26-19:58 MSK после hotfix; интервалы 0/1/2/4/8/16 минут, PID 88039 и runs=1. Причина - неизменный `history unverifiable` barrier ошибочно считался временным retry. После нового запуска была одна стартовая попытка в 21:06:05 MSK; за последующие 2 минуты она не повторилась, PID 47217 остался тем же. Targeted race и 20x regression PASS. |

## Crash-loop hotfix receipt - 2026-09-10 19:27 MSK

- За проверенное окно наблюдалось минимум 222 самостоятельных завершения Bria и
  444 `session.recovery_failed`; launchd дошёл как минимум до 300 запусков.
  Luna каждый раз становилась ready и причиной падения не была.
- RED/GREEN закрывает обе границы: после durable admission recovery-input больше
  не строит неподписанную legacy keyboard projection, а coordinator применяет
  unknown callback recovery только к callback update.
- Commit `eeb4bb5a444c6eac2af52bdaf6d1ccad85205a6d` находится в `origin/main`.
  Полный локальный `make check-full`, Stage 1 run `34501685253` и Platform run
  `34501685136` прошли.
- Установлена версия `20260910-recovery-poison-hotfix`; hashes `bria`, Codex и
  Claude adapter совпали с проверенной сборкой. После запуска PID 88039 имеет
  `runs=1` и `last exit code = never exited`, checkpoint продвинулся с
  `783531724` до `783531727`, Telegram identity OK. Config/settings/plist hashes
  сохранены; state и journal изменились только ожидаемым live-прогрессом.
- Два `session.recovery_failed` на старте относятся к действительно
  `awaiting_recovery` основной сессии и после 16:26:35 UTC не повторялись.

## Persistent preprocessing satellites release receipt - 2026-09-10 21:08 MSK

- Commit `b573960539908276731e8be4f42278c6e6aad049` опубликован в
  `origin/main`. Полный локальный gate, Stage 1 run `34511754264` и Platform
  run `34511753964` прошли для этого SHA.
- Установлена версия `20260910-persistent-preprocess-satellites`; SHA-256 всех
  трёх установленных бинарников совпали с проверенной сборкой. Новый PID 47217
  имеет `runs=2` с учётом единственного согласованного restart и единолично
  держит instance lock. Config/state и identity-only Telegram probes прошли.
- До и после установки сохранены 8 основных сессий и 31 journal input;
  config, settings и launchd plist побайтно не изменились. Legacy settings v5
  с `preprocessing_enabled=true` прочитаны как новый default `shared` без
  принудительной записи миграции.
- Private binding store имеет mode 0600: семь записей сохраняют lifecycle
  основных сессий, одна рабочая запись `shared` содержит единственный provider
  thread. Luna `gpt-5.6-luna` перешла `starting -> ready` за 1205 ms.
- Единственный стартовый `session.recovery_failed` для ранее проблемной
  `awaiting_recovery` сессии не повторился за 2 минуты; service PID и lock не
  менялись. Живой пользовательский preprocessing prompt и ручной Telegram UI
  не отправлялись в рамках postflight.
