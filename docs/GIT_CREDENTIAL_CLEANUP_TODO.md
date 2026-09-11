# A45: Git credential cleanup

## Договор задачи

- Результат: определить, был ли credential опубликован в Git, удалить
  подтверждённый источник риска и не выводить значение секрета.
- Источники: GitHub secret-scanning metadata, 84 достижимых commit текущего
  `Time4Mind/bria`, GitHub Actions logs и текущие workflow.
- Единица: один Git blob либо один `actions/checkout` step; период - вся
  достижимая история по 2026-09-11, timezone Europe/Moscow.
- Исключения: не читать значения локальных credentials, не менять secrets и не
  переписывать remote history без доказанного опубликованного секрета.
- Writes: workflow, repository policy/test, обычный commit/push/CI и standing
  release только текущего репозитория и Bria v2 service.
- Приёмка: GitHub alerts и high-confidence history scan не находят секретов;
  все checkout steps явно используют `persist-credentials: false`; policy,
  полный gate и exact-SHA CI проходят.

## Проверено и исправлено

| ID | Проверка | Результат |
|---|---|---|
| A45.1 | GitHub secret-scanning alerts | 0 alerts; значений секретов не запрашивалось и не выводилось |
| A45.2 | 84 reachable commit по private key, AWS, GitHub, provider, Telegram и bearer signatures | 0 совпадений; опубликованный credential не подтверждён |
| A45.3 | Источник строки CI `persist-credentials: true` | Начальный commit `a5c79ff` добавил checkout steps без override; GitHub временно сохранял job token только в runner git-config и удалял после job |
| A45.4 | Текущая конфигурация | Все 10 checkout steps имеют `persist-credentials: false`; policy-test отклоняет будущий пропуск |
| A45.5 | Ложный bearer alert на `bin/bria` | Release-artifact scan требует `Authorization: Bearer ...`, source scan остаётся строгим; реальный собранный trio проходит повторный policy scan |
| A45.R | Полный выпуск | полный `make check-full` GREEN; Git/CI/redeploy выполняются |

Переписывание истории не требуется: ни actual secret, ни secret-bearing blob не
обнаружены. Старый workflow хранит только настройку checkout по умолчанию, а не
значение `GITHUB_TOKEN`. Очистка меняет будущие jobs; старые CI runner credentials
были эфемерными и уже удалялись post-job самим checkout action.
