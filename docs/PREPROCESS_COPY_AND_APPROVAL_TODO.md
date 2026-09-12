# A54 - копируемая инструкция preprocessing и пропущенный approval

## Договор задачи

- Результат: действие изменения preprocessing показывает текущую эффективную
  инструкцию как многострочный копируемый Rich Markdown fenced-блок без языка;
  текущий необработанный Codex approval разобран по live evidence и, если это
  дефект Bria, исправлен воспроизводящим regression.
- Источник: указание Артёма 2026-09-12, Europe/Moscow, текущий код новой Bria и
  текущие safe-логи/terminal screen локального сервиса.
- Единица: одна карточка текущей инструкции и каждый распознанный текущий Codex
  command/file-edit approval при включённом `auto_approve_commands`.
- Включено: `internal/telegramsettings`, `internal/nativeapproval` и необходимая
  непосредственная flow-интеграция, тесты, документация и обычный выпуск.
- Исключено: ручные Telegram-сообщения, изменение live settings/state, постоянные
  правила Codex, approvals иных типов и раскрытие команды/reason в evidence.
- Acceptance: пользовательски наблюдаемый RED/GREEN для fenced instruction;
  сохранённый обезличенный live approval воспроизводит parser/flow RED и GREEN;
  focused race, полный `make check-full`, exact-SHA CI, signed install и service
  postflight проходят при сохранённых config/settings и совместимых state/journal.

## Карта покрытия

| ID | Зона | Владелец | Критерий остановки |
|---|---|---|---|
| A54.UI | Copyable current instruction | UI worker, `internal/telegramsettingsview`; integration owner - wire normalization | RED/GREEN и package race; fenced block без language tag |
| A54.AP | Live approval RCA и parser/flow | integration owner | safe live capture, verified cause, regression RED/GREEN |
| A54.REL | Integration и release | integration owner | full gate, independent review, CI, deploy/postflight |
| A54.REV | Read-only review | independent reviewer | findings устранены либо явно открыты |

## Статус

| ID | Состояние | Доказательство |
|---|---|---|
| A54.UI | done locally | пользователь подтвердил вариант 5; fenced no-language block сохраняется wire-level как `<pre><code>`, pagination/UTF-8/escaping и package race GREEN |
| A54.AP | done locally; live postflight pending | exact ignored 2180-byte live screen parser GREEN, setting ON, но нового `native.approval` нет; production-shaped blocked-custody regression и split workers GREEN |
| A54.REL | release pending | финальный `make check-full` GREEN |
| A54.REV | done | повторное независимое ревью: blocking findings нет; wire/production-shaped fixes приняты |

## Live evidence и причина

- Активная `TokenAudit` физически связана с живым Codex/tmux и generation 270;
  `auto_approve_commands=true`.
- Точный screen сохранён только в `.cache/private-approval-evidence` с `0600` и
  исключён из Git. Production parser распознаёт его как полный file-edit approval
  с тремя вариантами; command, reason и destination не публикуются.
- В текущем run были две более ранние попытки для этой generation (`uncertain`,
  затем другой fingerprint `confirmed`), но для ныне видимого fingerprint нового
  события нет: текущий сбой расположен до попытки Enter. Точная заблокированная
  операция старым логированием не различается.
- `StartNativeObserver` последовательно выполнял approval scan и projection
  custody в одном worker. Production-shaped regression блокирует durable custody
  предыдущего interactive screen: на старом коде следующий approval не получает
  `NativeControl`, после независимого coalesced fan-out/двух последовательных
  workers получает ровно один fenced `approve_once`; startup scan выполняется
  сразу, а не после первого секундного tick.
