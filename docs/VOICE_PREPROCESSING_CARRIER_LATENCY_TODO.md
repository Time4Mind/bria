# A49: voice preprocessing carrier latency

## Договор задачи

- Результат: убрать Bria-only задержку между готовым результатом Luna и
  передачей голосового запроса основной модели. Все статусы одного Telegram
  update обновляют его новую карточку без повторных 10-секундных ожиданий.
- Источники: live safe trace update `783532007`, durable output receipts,
  текущий carrier guard и Telegram flow; пользовательский payload исключён.
- Grain/population: один voice input и последовательность `recognized ->
  preprocessed -> provider accepted`; другой input, navigation и replacement
  carrier остаются stale и не редактируются.
- Writes: product code, tests и docs текущего репозитория. После полной
  приёмки действует standing release в `origin/main` и restart только
  `gui/501/com.time4mind.bria.v2`.
- Приёмка: behavioral RED/GREEN доказывает последовательные статусы одного
  update без ожидания, соседний guard другого update остаётся закрытым;
  telemetry различает match/supersede/timeout; focused/race/full gate,
  exact-SHA CI, install/restart и safe live postflight GREEN. Оставшаяся
  end-to-end voice latency измерена по этапам и сопоставлена с ccbot.
- Дополнение: перед публикацией новой карточки старая теряет все кнопки; на ней
  допустима только одна доказанно работающая кнопка перехода к самой свежей
  карточке, иначе keyboard пуст. Уведомление о финале фоновой сессии содержит
  её пользовательское имя.

## Coverage map

| ID | Зона | Owner | Stop criterion | Статус |
|---|---|---|---|---|
| A49.1 | Canonical input-carrier identity | integration owner; `inputcarrierguard` и prompt delivery tests | второй status того же update проходит немедленно, другой update блокируется | implemented, verified locally |
| A49.2 | Safe voice/guard telemetry | integration owner; trace wiring | preparation/guard outcome и duration видны без payload/raw identity | implemented, verified locally |
| A49.3 | Persisted-state и ccbot comparison | read-only reviewers | rollback/replay риски и остаточная latency классифицированы | implemented; two independent reviews APPROVE |
| A49.4 | Retire previous card keyboard | integration owner; Telegram flow | до новой карточки старая не сохраняет stale actions; latest-link либо единственная, либо keyboard пуст | implemented; retry/reopen/ABA focused race GREEN |
| A49.5 | Background completion identity | integration owner; completion path | уведомление содержит актуальное пользовательское имя завершившейся сессии | implemented; rename/missing-name tests GREEN |
| A49.R | Release | integration owner | full gate, exact-SHA CI, verified install и live postflight | pending |

## Live evidence

- Update `783532007`: ingress `2026-09-11 21:09:41.150 MSK`, Luna responded за
  `3356 ms`, preprocessing completed в `21:09:46.187`, а primary Codex
  `task_started` только в `21:10:05.458`: лишний промежуток `19.271 s`.
- Durable receipts для recognized-card и preprocessed status имеют
  `suppressed`. После первого status edit `LastPresentationOperation` хранит
  полный status operation, тогда как guard сравнивает его только с базовым
  `status:<update_id>`. Два последовательных mismatch проходят через bounded
  `10 s` wait и объясняют наблюдаемый разрыв.

## Реализация и локальная проверка

- `telegramstate.Card.CarrierOperation` хранит владельца физического carrier,
  меняется только вместе с carrier и переживает FileStore reopen. Старый JSON
  без поля читается и использует fail-closed fallback на
  `LastPresentationOperation`; версия state не менялась.
- Guard сопоставляет все prompt-status одного Telegram update с persisted
  owner. RED: внешний same-carrier edit между статусами приводил к deadline за
  `200 ms`; GREEN: `recognized -> preprocessed -> accepted` трижды обновляет
  carrier `77` без suppression. Другой update ждёт собственный carrier, новый
  carrier его supersede-ит, timeout не редактирует старую карточку.
- Payload-free trace пишет HMAC references и bounded duration для
  `prompt.carrier_guard` и `voice.preparation`; raw operation/session/card и
  распознанный текст не сохраняются. Existing preprocessing/provider/delivery
  traces завершают раскладку end-to-end задержки.
- Перед каждым replacement-card точный прежний carrier получает пустую
  keyboard, затем его callback-токены инвалидируются, и только после этого
  отправляется новая карточка. Stale replay не трогает уже заменивший её
  carrier; ABA защищён сохранённой carrier revision. Ошибка retirement оставляет
  durable status в `queued` до retry. Legacy-запись без доказуемой revision не
  трогает текущую карточку и завершается как superseded; exact pending-final
  custody при этом снимается, чтобы не блокировать следующие обновления.
- Фоновый final теперь сообщает `Фоновая сессия «<имя>» завершена.`. Имя
  берётся из актуального session display name; generic-уведомление при пустом
  имени не отправляется.
- Focused tests, affected packages и `cmd/bria` под `-race`, architecture,
  policy и release supply-chain - GREEN. Два независимых reviewer-а дали
  APPROVE после проверки ABA, exact pending-final custody и legacy replay без
  повторного snapshot. Финальный
  `VERSION=20260911-voice-carrier-latency make check-full` на итоговом дереве
  GREEN, включая global race и executable trio. Exact-SHA CI, deploy и новый
  live voice sample остаются в A49.R.

## Сравнение с ccbot

- ccbot не использует Luna preprocessing: после локального Parakeet transcript
  передаётся в основной Codex flow. Однако полный путь `STT -> provider` не
  измерен: перед tmux submit ccbot синхронно ждёт Telegram `resume_card_view`,
  включая возможный edit/send карточки.
- По доступным ccbot samples распознавание занимало `4.06-5.03 s`.
  Наблюдаемые `~2.77 s` относятся только к последующему tmux submit/proof:
  этот участок включает фиксированные ожидания `0.5 s` перед Enter и `2 s`
  перед проверкой Codex prompt. Это не полная задержка от окончания STT до
  приёма запроса provider.
- В исходном Bria sample распознавание было не дольше `1.65 s`, Luna `3.356 s`,
  а дефект carrier guard добавил `19.271 s` до старта primary. После выпуска
  новый live sample должен показать исчезновение этого искусственного разрыва;
  остаток свыше `2 s`, не объяснённый измеренными фазами, остаётся дефектом.
