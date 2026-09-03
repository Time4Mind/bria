# Bria

Bria - личный Telegram-интерфейс для работы с сессиями Codex и Claude на одном или нескольких компьютерах. Один вручную назначенный компьютер координирует Telegram, остальные могут выполнять задачи. Проект рассчитан на инфраструктуру владельца и не требует сторонней службы-посредника.

## Статус

Этап 2 продолжается; готовность к выпуску не заявляется. Текущее состояние классифицировано по доказательствам:

- **Production-wired / локальные тесты:** `cmd/bria run` компонует Bot API transport, owner/chat gate, атомарное durable state, очередь и journal, session lifecycle/recovery/supervision, Codex/Claude adapters, signed callback flow, provider interactions, authorization boundary, receipts и безопасные журналы. Эти границы покрыты модульными и интеграционными тестами текущего репозитория.
- **Только компонентные тесты:** media/file custody, speech preparation, backup/restore, update/rollback и сетевой multi-computer runtime имеют локальные компоненты и проверки, но не подключены к пользовательскому `run` как полный сквозной продуктовый путь.
- **External-unverified:** реальный пользовательский Telegram flow, реальные provider submit/authorization, доставка медиа и файлов через внешнюю систему, multi-computer/executor role, Linux/WSL/Docker и физические backup/update probes не подтверждены внешними receipts.

Исторические источники не являются доказательством текущего состояния. Новый проект не разделяет сессии по происхождению: в интерфейсе есть только сессии Codex и Claude.

Локальная сборка и проверка текущего состояния:

```sh
make check-full
bin/bria --help
bin/bria version
bin/bria check-config --config /absolute/path/to/config.json
bin/bria check-telegram --config /absolute/path/to/config.json
bin/bria run --config /absolute/path/to/config.json
```

`make check-full` собирает три соседних исполняемых файла: `bin/bria`, `bin/bria-codex-adapter` и `bin/bria-claude-adapter`. `check-config` проверяет локальную конфигурацию, секретные файлы и композицию исполнителей без обращения к Telegram. `check-telegram` выполняет только проверку идентичности бота через `getMe` и не забирает очередь обновлений. `run` захватывает блокировку экземпляра и запускает рабочий Telegram loop; при первом запуске он устанавливает сохраняемый backlog fence, поэтому это уже не безвредная проверка конфигурации.

## Границы первой версии

- один владелец и один личный чат в Telegram;
- Codex и Claude как равноправные исполнители;
- один или несколько связанных компьютеров с координатором, назначаемым только вручную;
- macOS, Linux, WSL (Linux-среда внутри Windows) и Docker;
- весь проект, включая координатор и исполнителей, может работать в Docker;
- автоматического выбора координатора, многопользовательского режима и сторонних сетевых служб нет.

## Документы

- [Назначение и границы продукта](docs/PRODUCT.md)
- [Принятые решения](docs/DECISIONS.md)
- [Сравнение CCBot и прежней Bria](docs/SOURCE_COMPARISON.md)
- [Архитектура](docs/ARCHITECTURE.md)
- [Жизненный цикл сессий](docs/SESSION_LIFECYCLE.md)
- [Интерфейс Telegram](docs/TELEGRAM_UX.md)
- [Надёжность и резервное копирование](docs/RELIABILITY_AND_BACKUP.md)
- [Безопасность](docs/SECURITY.md)
- [Платформы и развёртывание](docs/PLATFORMS_AND_DEPLOYMENT.md)
- [Проверки и критерии готовности](docs/TESTING_AND_ACCEPTANCE.md)
- [План реализации](docs/IMPLEMENTATION_PLAN.md)
- [Handoff: статус и следующий план](docs/STATUS_AND_NEXT.md)

Правила обязательной параллельной разработки несколькими агентами находятся в [AGENTS.md](AGENTS.md). Автоматическая проверка этих правил описана в [scripts/check_repo.go](scripts/check_repo.go) и запускается через [.github/workflows/context.yml](.github/workflows/context.yml).
