# umir-warehouse

Сервис склада одного ресторана.

Реализованы публичные типы warehouse, чистые расчёты количества, срока годности, FEFO и упорядоченного заказа, журнал событий и атомарные EventStore/SnapshotStore в памяти, единый владелец проекции склада, списание просроченных партий, справочники, поставки, оформление заказов и запросы остатков, партий к истечению и истории.

Хранение продуктов, блюд и рецептур, приём поставок и партий, точные остатки, FEFO, сроки годности, списание просрочки и мониторинг. Warehouse владеет справочниками, изменениями склада, событиями и снапшотами. Окончательная продажа и расход ингредиентов фиксируются атомарно.

Основной документ бизнес-контрактов находится в [docs фабрики](https://github.com/JustIkra/umir-factory/blob/main/docs/business-core.md). При расхождении описаний контрактов приоритет имеет docs.

## Текущее состояние

Работают отдельные публичный и внутренний HTTP listeners. Публичный listener обслуживает `GET /healthz`, `/inventory`, `/expiring-batches`, `/history`, `POST /deliveries` и `POST /expiration-runs`. Внутренний listener на `INTERNAL_PORT` обслуживает `/internal/v1` для каталога, меню, оценок заказа, оформления и чтения сохранённых исходов. Bootstrap экспортирует сборку обоих обработчиков на одном Runtime. Gateway вызывает операцию просрочки только по публичному адресу.

HTTP-код преобразует wire DTO в contract-типы и обратно. JSON тела ограничены `MAX_REQUEST_BYTES`; неизвестные и повторные поля, `null`, неверная кодировка, второе JSON-значение, неверные числа/время и неоднозначные query отклоняются до вызова application. Количества и номера строк — канонические положительные десятичные строки int64; время — RFC3339 с секундами и `+03:00`. Ошибки имеют поля `code`, `message`, `outcome`.

`POST /expiration-runs` принимает только нулевое тело и query-параметры не допускает. Успешный ответ содержит `evaluatedAt`, десятичную строку `stateVersion` и массив `expired`; элемент массива использует wire-поля результата списания (`batchID`, `productID`, `writtenOffQuantity`, `reason`, `expiresAt`, `processedAt`). Метаданные и результаты захватываются одним запуском подготовки Owner. Ошибка подготовки возвращает `InventoryUnavailable` с `outcome: unknown`, так как предыдущие списания этого запуска могли быть записаны.

Склад резервирует `commandID` подтверждения до вызова Owner. Повтор во время реально выполняющегося вызова получает `Pending`; после завершения сохраняются неизменяемые `OrderCommitted`, `RecheckRequired` или определённый отказ. Неопределённая ошибка остаётся `Pending`, а `GET /internal/v1/order-attempts` только читает состояние и никогда не запускает оформление. Идентификаторы попыток не вытесняются, пока жив процесс.

Application регистрирует неизменяемые определения, принимает поставки, независимо оценивает меню и совместно оценивает упорядоченные позиции заказа. Commit повторно проверяет остатки, возвращает `RecheckRequired` при уменьшении принятого состава и иначе записывает точные FEFO-списания одной командой. Запросы остатков, партий к истечению и истории возвращают отдельные значения без ссылок на проекцию. Свежие оценки используют подготовленное состояние и общий `evaluatedAt/stateVersion`; `GetDish` возвращает рецепт с метаданными той же версии и времени захвата. Меню оценивает каждое блюдо независимо на одну порцию и возвращает полные распределения. EventStore хранит журнал только в памяти процесса. Owner можно до первого восстановления настроить на SnapshotStore и положительный порог событий; он предлагает полные копии состояния после опубликованных команд. SnapshotWorker предоставляет явные `RunSnapshots(ctx)` и `RunSnapshotOnce(ctx)`; создание Owner не запускает goroutine. `cmd/server` запускает worker отдельно от HTTP. После ошибки сохранения снапшота процесс ждёт `SNAPSHOT_RETRY_INTERVAL` и перезапускает worker; HTTP продолжает работать. Просроченные партии обрабатываются при свежих операциях склада и через явный `POST /expiration-runs`; warehouse не запускает собственный периодический worker. Восстановление проверяет кандидата полным replay соответствующего префикса через тот же reducer, сравнивает итоговое типизированное состояние и затем применяет суффикс от снапшота. Поэтому снапшоты сейчас не сокращают чтение или применение журнала и служат проверенной точкой восстановления. Срок задаётся в днях по 24 часа или в календарных месяцах; количество хранится в тысячных долях базовой единицы.

## Бизнес-правила

- У продукта одна базовая единица: g, ml или pcs. Количество хранится как знаковое 64-битное целое число в тысячных единицы; остатки неотрицательны.
- Поставка отклоняется, если после применения всей команды сумма живых остатков продукта выходит за диапазон int64. Ошибка сохраняет причину `internal/domain.ErrQuantityOverflow`; проверка учитывает немедленные списания этой же команды и сначала применяет наступившие списания существующих партий через общий механизм подготовки.
- Один deliveryID создаёт один приход независимо от commandID. Повтор записанной команды возвращает прежний результат; новая команда для принятого deliveryID возвращает DeliveryAlreadyReceived. Проверка уникальности и запись используют одну версию состояния.
- Строка поступления требует productID, manufacturedAt, shelfLife.value, shelfLife.unit, receivedAt и quantity. Срок задаётся положительным числом суток или календарных месяцев. Даты содержат время и московское смещение +03:00. Даты и сроки проверяются до создания событий; у уже просроченной партии атомарно записываются поступление и списание.
- Рецептура блюда содержит уникальные productID с положительным количеством на порцию. В `DefineDish` поле `Ingredient.BaseUnit` служит необязательной подсказкой: пустое значение допустимо, заданное должно совпадать с единицей продукта. Ответы каталога всегда берут базовую единицу из зарегистрированного продукта.
- Годность отсчитывается от изготовления. Единицы срока: day = 24 часа, month = календарный месяц с сохранением времени суток и сокращением дня до конца целевого месяца при необходимости. Всё значение срока прибавляется к исходной дате одним расчётом. expiresAt сохраняется в событии; входная строка его не принимает.
- Прямой Go AddDate переносит лишние дни в следующий месяц. Правило конца месяца требует отдельного календарного расчёта; day прибавляет фиксированную длительность с проверкой переполнения.
- Обработка срока принадлежит общему механизму очереди: при времени обработки >= dueAt выполняется ExpireBatch, записывается BatchExpired и обнуляется остаток проекции. FEFO использует положительные остатки подготовленной проекции в порядке expiresAt, receivedAt, batchID; отдельной проверки даты на Batch нет.
- Просрочка списывает только оставшееся количество, сохраняет операцию в истории и завершает задачу после записи. Повторная обработка не должна создавать второе списание.
- Очередь истечения — восстанавливаемая проекция событий с положительными остатками и dueAt из DeliveryReceived. Остатки и индекс публикуются вместе после полного набора команды. Обычная подготовка проверяет пригодность/версию и выполняет наступившие списания; полная сверка нужна при восстановлении или известном сбое. Пустой индекс допустим только в полной пригодной проекции. Пропущенный тик или раннее поступление заказа этот порядок не обходят.
- При ошибке подготовки или записи обязательного списания новая операция остатков получает InventoryUnavailable. OrderPlaced и расход такого заказа не записываются. Сбой индекса после успешной записи не отменяет записанную команду; уже оформленный заказ возвращается по идентификаторам до подготовки очереди.
- Оформленная продажа сохраняет принятый состав и точные списания по партиям. Восстановление применяет записанные факты; снапшот привязан к версии потока.
- Для чистых расчётов чтения неизменяемое состояние захватывается вместе с evaluatedAt/stateVersion внутри допуска, затем расчёт выполняется вне сериализации. Новое оформление повторно проверяет актуальный склад.
- Снапшот захватывается после полной команды по порогу событий и сохраняется в фоне через ограниченную очередь. Точка начала восстановления меняется только после успешного сохранения; рабочие остатки обновляются после каждой команды. Сбой сохранения не отменяет записанную операцию.
- Неопределённый результат записи разрешается повтором исходных событий с теми же eventID и expectedVersion. Подтверждённый конфликт версии требует перечитать состояние, повторить подготовку и получить новое время оценки. Идемпотентность commandID/отпечатка обеспечивает адаптер EventStore.

Batch хранит неизменное receivedQuantity и изменяемое remainingQuantity. Строки поставки, ShelfLife и Allocation — вложенные значения. InventoryState восстанавливает справочники и индексы поставок, партий, заказов и записанных команд из событий. Для часов и генератора ID достаточно функций; отдельный интерфейс очереди не требуется. EventStore, SnapshotStore и порт warehouse задают заменяемые границы тестового каркаса. Справочники хранятся в едином потоке warehouse; MenuService в serving работает с ними через порт.

## Разработка

### Структура бизнес-кода

Сценарии склада и адаптеры в памяти реализуют [business-core](https://github.com/JustIkra/umir-factory/blob/main/docs/business-core.md). [Архитектура Go](https://github.com/JustIkra/umir-factory/blob/main/docs/go-architecture.md) описывает направление импортов и проверочную сборку. HTTP-каркас находится в `cmd/server/main.go`.

| Каталог | Ответственность |
| --- | --- |
| `internal/domain` | Quantity/ShelfLife, определения, события, InventoryState, reducer и чистые расчёты FEFO и заказа. |
| `internal/application` | Порты EventStore/SnapshotStore; единый владелец InventoryState, восстановление и допуск истечения; каталог, поставки, оценки меню и заказа, оформление заказа и запросы склада. |
| `internal/adapters/memory` | EventStore хранит полные наборы команд; SnapshotStore проверяет и атомарно публикует снапшоты в памяти. |
| `internal/adapters/httpapi` | Public/internal listeners, строгие JSON codecs, wire DTO и HTTP status/error mapping. |
| `contract` | Публичные значения команд, запросов, результатов и ошибок склада. |
| `bootstrap` | Конкретный Runtime для сборки и вызова сценариев в памяти; функции часов/ID и настройки. |
| `tests/testsupport` | Построители фикстур, часы и последовательности ID; production их не импортирует. |

Application не импортирует адаптеры или serving. Bootstrap собирает зависимости и переводит публичные значения; бизнес-правила остаются в domain/application. Интерфейсы вводятся на заменяемых границах, конкретным логическим Service отдельные интерфейсы не нужны. Все обработчики склада используют одну сериализацию; деление по каталогам не делит владение остатками.

`bootstrap.NewInMemory(Options)` создаёт Runtime с пустыми EventStore/SnapshotStore. Options требует ненулевые Now и NewID и положительный EventsPerSnapshot. Конструктор не вызывает зависимости и не запускает goroutine. Runtime предоставляет каталог, поставки, оценки и оформление заказа, запросы, явную `RunExpirations` с общей версией для времени и списка результатов, восстановление, lookup записанных результатов и явные RunSnapshotOnce/RunSnapshotWorker. Существующий `ProcessDueExpirations` остаётся доступен Go-клиентам и использует ту же подготовку. Полная таблица приёмочных сценариев находится в разделе 12 business-core.

Lookup возвращает записанные факты без подготовки текущей проекции. У записанного Dish ингредиенты содержат productID и quantity; необязательное поле BaseUnit остаётся пустым. `GetDish` и `GetDishCatalog` возвращают единицы зарегистрированных продуктов; `GetDish` дополнительно возвращает `contract.DishDetails` с `EvaluatedAt`, `StateVersion` и вложенным `Dish`. Результат `DefineDish` остаётся неизменённым. Общий отпечаток ConfirmOrder использует SHA-256 от типа команды, NUL и JSON с полем proposalID.

Совместные тесты в factory вызывают только contract/bootstrap через bridge, который реализует порты serving. В production warehouse не зависит от соседнего Go-модуля. Прямой импорт чужих internal запрещён, даже в Go workspace.

Готовность этапа подтверждается контрактными тестами реальных хранилищ в памяти, собственными тестами сценариев, race detector и совместным тестовым процессом. Наличие healthz или успешная Docker-сборка не подтверждают правила склада. HTTP-команды, постоянная база и стек observability относятся к последующим этапам.

`cmd/server` собирает и восстанавливает Runtime до запуска HTTP listeners, затем запускает snapshot worker и явно отменяет и ожидает его завершения при остановке процесса. Остановка завершает оба listeners. Dockerfile включает cmd, contract, bootstrap и internal. Тестовая поддержка в образ не копируется. `GET /healthz` проверяет процесс; он не подтверждает полную бизнес-приёмку.

Журнал, снимки и все данные существуют только в памяти процесса. После его окончания они теряются. SnapshotStore не заменяет постоянное хранение. Восстановление проверяет весь префикс журнала через reducer, поэтому сохранённый снимок сейчас не сокращает чтение или CPU replay.

### Доступные проверки

Требуется Go 1.26.9.

```sh
make tools
make check
GOWORK=off go vet ./...
TZ=Europe/Moscow PORT=18083 go run ./cmd/server
```

`make check` проверяет форматирование, сборку, закреплённые golangci-lint/go-arch-lint и полные race-тесты с GOWORK=off. Версии инструментов находятся в `.go-quality-versions`; правила — в `.golangci.yml` и `.go-arch-lint.yml`. Для отдельных файлов используйте `make fmt FILES='path/to/file.go'`; список нескольких путей разделяется новой строкой. Межсервисные проверки запускаются через `umir-checks business --local` из фабрики.

При запуске через Go healthz доступен на `http://127.0.0.1:18083/healthz`.

Сборка отдельного контейнера. Standalone Compose публикует только публичный listener на host `18083`; внутренний порт `8082` остаётся доступен лишь внутри container network.

```sh
docker compose up --build -d --wait
```

Локальный Compose публикует тот же порт 18083. В общей сборке фабрики сервис доступен только внутри сети на `warehouse:8080`.

| Настройка | Значение в контейнере |
| --- | --- |
| `PORT` | `8080` |
| `INTERNAL_PORT` | `8082`; должен отличаться от `PORT` |
| `TZ` | `Europe/Moscow` |
| `EVENTS_PER_SNAPSHOT` | `100`; положительное int64 |
| `SNAPSHOT_RETRY_INTERVAL` | `1s`; положительная Go duration; задержка перед повтором после ошибки SnapshotWorker |
| `REQUEST_TIMEOUT` | `8s`; положительная Go duration |
| `MAX_REQUEST_BYTES` | `1048576`; от 1 до 67108864 байт |

Нулевые, отрицательные, некорректные и переполненные настройки отклоняются при старте. `EXPIRATION_CHECK_INTERVAL` больше не читается. Gateway управляет расписанием вызовов `POST /expiration-runs`; любая свежая операция склада также обрабатывает наступившую просрочку.

Общая сборка и команды запуска описаны в [README фабрики](https://github.com/JustIkra/umir-factory#readme).

## Совместимость и версии

Публичная поверхность — экспортируемые значения и ошибки `contract`, конструкторы и методы `bootstrap`, маршруты HTTP, их wire fields/status/outcome и настройки процесса. Внутренний API доступен только через отдельный listener; gateway подключается к публичному. `bootstrap.NewHTTPHandlers` требует положительные request timeout и body limit. Вызовы `GetDish` возвращают `contract.DishDetails`; Go-клиенты читают рецепт через поле `Dish`, а время и версию — через `EvaluatedAt` и `StateVersion`. Порты EventStore/SnapshotStore принадлежат внутреннему application; заменяющий адаптер обязан сохранять атомарность и семантику неизвестного исхода.

Используется SemVer. До 1.0 совместимые исправления получают patch, новые возможности или несовместимые изменения — minor с описанием миграции. После 1.0 несовместимые изменения требуют major. Изменения остаются в CHANGELOG под «Не выпущено» до выпуска. Теги корневого модуля имеют вид `vX.Y.Z`; выпущенные теги не изменяются. Образы и фабрика сохраняют точные SHA.

## Совместная работа

Main защищён активным ruleset. Обновлять main и объединять PR может только `JustIkra`.
`Kxqusos` добавлен с ролью write и работает в других ветках.
Остальные пользователи GitHub могут создать issue, fork и PR.
Владелец кода указан в `.github/CODEOWNERS`.

Перед PR выполните локальные проверки. Изменение межсервисного контракта согласуйте с вызывающей стороной.

## CI/CD

[GitHub Actions](https://github.com/JustIkra/umir-warehouse/actions) проверяет push в ветки, PR в main и ручные запуски.
После успешной проверки main публикует `ghcr.io/justikra/umir-warehouse:sha-<полный SHA коммита>` для `linux/amd64` и `linux/arm64`.
Публикация использует `GITHUB_TOKEN`, с `packages:write` только у задания публикации.
Коммит документации запускает тот же workflow.

[Фабрика](https://github.com/JustIkra/umir-factory) закрепляет выбранный SHA в `compose.yaml`.
Обновление фабрики выполняется отдельным PR; новый образ не развёртывается автоматически.
Секреты передаются через окружение; `.env` исключён из Git.

## Development standard and releases

Repository instructions are in [AGENTS.md](AGENTS.md). Public compatibility covers documented requests, results, error states and operational interfaces; warehouse/serving also publish their declared Go contract surfaces when implemented. Internal refactoring does not automatically change this contract.

Go tools are pinned to golangci-lint 2.14.0 and go-arch-lint 1.19.0 in [.go-quality-versions](.go-quality-versions). Local and CI checks use the same commands:

```sh
make tools
make fmt FILES="cmd/server/main.go"
make check
make quality-test
```

`make check` verifies the module inventory, formatting without writes, build, lint, source dependency architecture and fresh race tests. The shared runner checks one root module (`.`). Declared nested modules fail explicitly and need separate effective quality gates. Library-only modules are supported; executable output stays in temporary storage. Tests and tool caches remain outside production dependencies. `make fmt-all` is reserved for a deliberate full formatting change. Service checks use `GOWORK=off`; unknown packages or modules fail. Empty future components need no placeholder packages. [Linter rules](.golangci.yml) constrain errors, imports and clock access; [architecture rules](.go-arch-lint.yml) cover the module root, including public contract/bootstrap packages.

Use [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/) and [Semantic Versioning](https://semver.org/spec/v2.0.0.html). Stable incompatible changes increment major, compatible additions minor and fixes patch. Our pre-1.0 convention uses patch for compatible fixes and minor for additions or breaking changes. Mark breaking behavior and migration explicitly.

Release repositories independently. Use annotated `vX.Y.Z` tags for actual releases; published nested Go modules prefix tags with the logical module subdirectory (excluding a trailing major-version directory), and Go v2+ needs the matching major import path. Published tags/versions stay immutable. Record changes under Unreleased before releasing. Preserve Umir `sha-<full-SHA>` image identities and exact factory component pins; SemVer does not replace them with `latest`. API versions and service releases are separate.

Preserve unrelated local edits, stage reviewed paths and check `git diff --cached` and `git diff --check`. Tagging, publishing and shared-history rewrites require the corresponding user instruction. This standard does not create a release or change the current version.
