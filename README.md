# umir-warehouse

FIN1 adds the typed `Runtime.SupplySuggestions` and `Runtime.RecommendationInputs` reads in scoped v2 mode. Their private routes are `GET /internal/warehouse/v2/r/{restaurantID}/supply-suggestions` and `GET /internal/warehouse/v2/r/{restaurantID}/recommendation-inputs`. The [versioned Factory supplement](https://github.com/JustIkra/umir-factory/blob/5a3ab9cb7a2a48801f07024d5b5c205577211e75/docs/completion-api-amendment.md) and its composed schemas define the contract. Supply requires the Gateway key, operation `supplySuggestions` and `stock.read`. Recommendation inputs require the Serving key, operation `recommendations`, the two-service audience and the narrow `consumer.recommendations.read` permission. These routes do not expose arbitrary order history.

Supply uses the previous 168 physical hours of recorded order allocations and human-confirmed writeoffs. Low coverage is strictly below three days; the stock target is seven days. A one-portion deficit can produce a suggestion without usage history. Deficits for the same product use the maximum, not the sum across dishes. Quantities retain the existing 0.001 base-unit quantum. Averages use the full seven-day denominator and display six decimal places in base units per day. The response labels incomplete epoch history and does not promise a purchase or production capacity.

Both reads capture the current validated projection and matching journal prefix under the owner lock. They scan the journal once for period facts, without replaying state or cloning historical orders/commands. Expired and pending batches are excluded immediately, including before the expiry scheduler records a transition. The reads append no events and never resolve pending mutations. An unavailable projection, unresolved owner command or mismatched journal prefix returns `InventoryUnavailable`; normal recovery or the original command retry owns recovery. Supply results are limited to 500 rows and 500 affected dishes per product. Recommendation inputs are limited to 10000 menu dishes, risk batches and demand rows. A serialized response over 16MiB returns `LimitExceeded`; partial success is not returned. Serving owns ranking; Warehouse returns coherent menu, risk and aggregate demand facts.

The UA1 owner addition serves `GET /internal/warehouse/v2/r/{restaurantID}/products/{productID}/batches` with `stock.read`, the Gateway caller key and a scoped JWT for `productBatches`. It returns all positive remaining batches in FEFO order, including pending writeoff, with exact quantities and recorded batch settings. Default page size is100, maximum500. Actor/scope/product/filter-bound captures last five physical minutes; limits are128 captures,10000 rows per capture and16MiB retained per restaurant. Runtime close stops capture timers and denies cursor reuse. `bootstrap.Options.PhysicalNow` injects the page clock independently of the business clock. Gateway composition and real-service UA1 qualification are separate stages.

The unpublished W1–W6 candidate implements the C2-frozen scoped backend contract `2.0.0-draft.2`. The selected legacy assembly and published versions remain separate. Sections that describe one restaurant, direct public business routes, or automatic final expiry apply to legacy mode. Scoped v2 requires an explicit restaurant, isolated in-memory state, pending expiry and authorized human confirmation. No legacy route selects a restaurant implicitly.

Сервис склада одного ресторана.

Реализованы публичные типы warehouse, чистые расчёты количества, срока годности, FEFO и упорядоченного заказа, журнал событий и атомарные EventStore/SnapshotStore в памяти, единый владелец проекции склада, списание просроченных партий, справочники, поставки, оформление заказов и запросы остатков, партий к истечению и истории.

Хранение продуктов, блюд и рецептур, приём поставок и партий, точные остатки, FEFO, сроки годности, списание просрочки и мониторинг. Warehouse владеет справочниками, изменениями склада, событиями и снапшотами. Окончательная продажа и расход ингредиентов фиксируются атомарно.

Основной документ бизнес-контрактов находится в [docs фабрики](https://github.com/JustIkra/umir-factory/blob/main/docs/business-core.md). При расхождении описаний контрактов приоритет имеет docs.

## Scale checks

`make perf` (build tag `perf`) builds a restaurant with 1000, 4000 and 8000 orders over the memory stores, prints the latency of commits, reads and recovery and the heap of a freshly restored owner, and fails only when recovery, `Movements` (CPU time) or the median commit grow faster than the corpus. The numbers are not absolute limits and depend on the machine.

## Restaurant boundary (W1 candidate)

The W1 candidate adds a separate v2 composition. It consumes the frozen backend contract 2.0.0-draft.2. `WAREHOUSE_API_MODE` has no default and the process refuses to start without it: set `v2` to select this composition or `legacy` explicitly for the v1 topology. Do not expose a legacy business listener as the v2 backend.

In v2 mode, the public listener serves only `GET /healthz`. All business requests use the private listener and `/internal/warehouse/v2/r/{restaurantID}/...`. Unscoped requests return InvalidArgument. Health reports process availability, not restaurant readiness.

| v2 configuration | Purpose |
| --- | --- |
| `AUTH_JWKS_URL` | Exact Auth discovery URL ending in `/internal/auth/v1/jwks`. |
| `AUTH_SERVICE_KEY` | Warehouse credential for Auth discovery. |
| `AUTH_RUNTIME_SERVICE_KEY` | Auth credential for the Warehouse lifecycle boundary. |
| `GATEWAY_SERVICE_KEY` | Gateway credential for the Warehouse private listener. |
| `SERVING_SERVICE_KEY` | Serving credential for the Warehouse private listener. |
| `<NAME>_FILE` | For each of the four service keys above, the path of a file that holds the key instead of the variable itself (a Docker or Kubernetes secret). One trailing newline is dropped. Setting both `<NAME>` and `<NAME>_FILE`, or pointing at an empty or unreadable file, stops the start; errors name the variable and never contain the key. |
| `AUTH_ALLOW_PLAIN_HTTP=true` | Allow HTTP discovery only on an isolated test network. HTTPS is required otherwise. |

V2 requires distinct, nonempty caller keys and a request limit of at most 1 MiB. It does not log token or key values. The Auth client rejects redirects, bounds discovery responses to 64 KiB, and validates one to three Ed25519 public keys. It refreshes every 30 seconds when used, with at most one extra refresh for an unknown kid in that window. Failed discovery blocks verification. Caller authentication uses constant-time comparisons. JWT validation checks exact EdDSA headers, claims, issuer, audiences, 60-second lifetime, subject, operation, capability, and route restaurant. Browser identity/cookie headers cannot supply trust.

`bootstrap.NewRegistry(Options, epoch)` starts empty. Explicit registration creates one Owner, EventStore, SnapshotStore and order-attempt coordinator per restaurant. Resolve checks restaurant, organization, lifecycle generation and the injected current process epoch. Reads and writes never create a tenant. `Register(ctx, contract.Scope)` is a trusted composition port. `RegisterRuntime(ctx, scope, operationID)` is the Auth lifecycle port. Both retain the same live runtime on duplicate registration; the Auth port also binds its saved operation ID and settings. A changed generation, organization, operation ID or registration settings cannot reset business state. Injected Now and NewID functions must support concurrent calls across restaurants.

`bootstrap.NewScopedHTTPHandler` injects the verifier and consumer-owned tenant directory. W1 installs catalog reads/writes, menu/order evaluation and passive recorded-command lookup. W3 adds physical stock, pending-writeoff reads, due runs and human confirmation with passive confirmation lookup. Every frozen Warehouse route passes through the trust boundary, including operations reserved for later units. Serving consumer access requires its own transport key and an Auth token that permits both Serving and Warehouse. A token for Serving product reads cannot authorize dish reads, public stock, delivery or writeoff. Scheduler authority permits only due expiry.

The frozen order consumer routes dispatch through the real owner. Order commits retain the original recorded time and version on exact replay. Passive orders lookup requires orderID; passive attempts lookup requires commandID only and reads the actual retained attempt coordinator. Legacy proposalID queries remain confined to legacy mode. Completed, rejected, pending and unseen states retain their owner meaning. A pending commit returns an unknown-outcome error; use passive resolution in the original epoch.

History accepts an optional canonical nonnegative fromSequence and returns up to 500 subsequent journal records in sequence order. Continue from the last returned sequence. Each record carries tenant and epoch, with the exact schema-1 or schema-2 event payload. Expiring batches uses the same 48-hour physical horizon as inventory, one real preparation, and expiry-time/batch-ID ordering. It excludes pending stock and publishes preparation invalidation headers. The frozen v2 route accepts no cutoffAt query.

Boundary failure outcomes follow the operation semantics. Order evaluation uses POST but writes no command: authoritative trust, epoch-input, readiness and request-validation failures return `unavailable`. Refused mutations return `not_committed`. A saved runtime epoch mismatch remains `RuntimeReset` with `indeterminate`; it is not a definite refusal of the original attempt.

Malformed restaurant IDs return `InvalidArgument` before authentication, runtime resolution or dispatch. An identifiable route determines the refusal outcome, including `unavailable` for POST order evaluation. Unsupported routes or methods retain the method-based outcome (`unavailable` for GET, `not_committed` otherwise): malformed restaurant input returns HTTP 400, while a valid restaurant ID returns HTTP 405.

W2 installs Auth-only PUT and DELETE at /internal/warehouse/v2/r/{restaurantID}/registry. Requests use the frozen RegisterRuntime or CloseRuntime body, an Auth service token and Auth transport key. The signed organization, restaurant, epoch and lifecycle generation must agree with the request. Successful replies contain restaurantID, service=warehouse, the current epoch, ready or closed state and the acknowledged lifecycleVersion. Exact registration retries preserve inventory. Close requires a later lifecycle generation than a live registration. Exact close retries finish the saved operation; a different close identity conflicts.

Close acceptance permanently marks the restaurant ID for this epoch and fences every retained Runtime handle. New calls return ScopeDeleted, including recovery, passive outcome lookups and snapshot operations. Previously admitted calls may finish. Cleanup cancels and joins snapshot work, waits for admitted calls, and removes the owner, journal, snapshots and attempt coordinator. A deadline or cancellation after acceptance leaves cleanup pending and the fence intact. Retry the same close operation to finish; do not interpret a lost or failed reply as cleanup completion. The registry never holds its global lock while it waits for draining. Neighboring restaurants remain usable. The snapshot worker owner ignores a deleted restaurant's normal termination and joins all workers at shutdown.

Auth signing-key cache validity uses the injected owner clock and a 30-second cache window. Failed discovery retries are throttled by a separate host monotonic clock at one second. RetryNow can inject that mechanism clock; it defaults to time.Now. A valid cache does not expire from host elapsed time. Owner-time expiry still refreshes immediately, and a failed refresh never serves stale keys. Fixture startup recovery requires no business-clock advance.

Auth remains the sole durable intent and tombstone authority. A fresh process has a new epoch and empty registry, state and outcome history. Auth reconciliation must send registration only for its active restaurants and close its deleting/deleted IDs before treating them as reconciled. Warehouse cannot restore stock or determine an old command outcome from empty state. Same-ID registration is permanently denied after a local close, even with a newer generation. The local tombstone lasts for this epoch; Auth restores cutoff after restart. These ports do not implement Auth's persistent retry owner or prove its service integration.

GET /internal/warehouse/v2/runtime requires a registered peer transport key and returns the current epoch, process readiness and tzdataVersion. Process readiness does not acknowledge any restaurant. Restaurant readiness is the exact register/close reply and remains subject to Auth's current-epoch decision. The process loads the packaged Go 1.26.9 IANA archive explicitly and reports go1.26.9-embedded. This archive contains IANA 2025c. Composition rejects a supplied version that does not identify this package. Missing or corrupt data fails readiness; host Local/UTC data does not substitute for it. Health remains separate. Coverage(ctx, scope) exposes the captured start of this restaurant's current in-memory history to later report assembly; duplicate registration preserves it and a new epoch starts new coverage.

The W2 lifecycle, W3 pending/final writeoff and scoped stock, W4 ID suggestions, and W5 captured calendar delivery and analytics surfaces are implemented. Remaining scoped route compatibility and the operational wrapper belong to W6. An unfinished operation returns explicit unavailable after authentication. This candidate does not establish complete service or real assembly acceptance.

Run focused lifecycle tests with `GOWORK=off go test -race -count=1 ./tests/integration -run TestW2`. Run focused boundary tests with `GOWORK=off go test -race -count=1 ./tests/integration ./internal/adapters/authkeys ./cmd/server -run TestW1`. Use `GOWORK=off make check` for the full module gates. Tests keep synthetic signing material and fixtures outside production source.

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

Нулевые, отрицательные, некорректные и переполненные настройки отклоняются при старте. `EXPIRATION_CHECK_INTERVAL` больше не читается; этот warehouse candidate требует scheduler от другого разработчика в gateway: до согласованного выпуска gateway candidate и обновления pin периодические списания отсутствуют, хотя свежие операции склада продолжают обрабатывать наступившую просрочку.

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

`make perf` runs the scale harness in `tests/perf` (build tag `perf`, so it is not part of `make check`): a corpus of 1000, 4000 and 8000 orders, a table of latencies and heap, and checks of growth ratios between the first and last checkpoint rather than absolute times.

Use [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/) and [Semantic Versioning](https://semver.org/spec/v2.0.0.html). Stable incompatible changes increment major, compatible additions minor and fixes patch. Our pre-1.0 convention uses patch for compatible fixes and minor for additions or breaking changes. Mark breaking behavior and migration explicitly.

Release repositories independently. Use annotated `vX.Y.Z` tags for actual releases; published nested Go modules prefix tags with the logical module subdirectory (excluding a trailing major-version directory), and Go v2+ needs the matching major import path. Published tags/versions stay immutable. Record changes under Unreleased before releasing. Preserve Umir `sha-<full-SHA>` image identities and exact factory component pins; SemVer does not replace them with `latest`. API versions and service releases are separate.

Preserve unrelated local edits, stage reviewed paths and check `git diff --cached` and `git diff --check`. Tagging, publishing and shared-history rewrites require the corresponding user instruction. This standard does not create a release or change the current version.

## Catalog ID suggestions (W4 candidate)

Warehouse owns both product and dish IDs. `GET /internal/warehouse/v2/r/{restaurantID}/id-suggestions?kind=product` returns the frozen `ScopedSuggestion` envelope. Use `kind=dish` for the separate dish catalog. Gateway caller authentication and an Auth user token with operation `suggestID` and capability `catalog.write` are required. Missing, duplicate, unknown or invalid query fields return `InvalidArgument`.

`bootstrap.Runtime.SuggestID(ctx, contract.CatalogKind)` reads the current catalog under its owner. The result contains `Kind`, `SuggestedID`, `Reserved=false`, `EvaluatedAt` and `StateVersion`. The suggested ID is the smallest free positive integer as a canonical decimal string. There is no fixed-width integer conversion. Occupancy uses exact opaque string equality: `01`, `1.0`, Unicode IDs and numbers above int64 remain valid manual IDs and do not occupy `1`. Product, dish and restaurant namespaces are independent.

Suggestions do not reserve an ID, append events, advance the business version or prepare due expiry. A failed catalog read returns unavailable. An unresolved owner command also returns `InventoryUnavailable`; a suggestion does not retry or resolve that command. A user can create another valid free ID. Creation checks the final supplied ID atomically and returns `ProductConflict` or `DishConflict` for a competing definition. Exact command replay returns the retained definition. After an unknown creation outcome, resolve or retry that original command in its original epoch before starting a fresh operation.

Run `go test -race -count=1 ./internal/application ./tests/integration -run TestW4` for focused coverage, and `make check` for the complete module gates. W4 does not establish real assembly acceptance. W6 adds the operational wrapper and verifies the complete route surface.

## Pending expiry and human confirmation (W3 candidate)

Scoped v2 runtimes use the injected owner clock. Registry supplies Options.Scope to NewInMemory; trusted standalone v2 composition must supply that scope explicitly. At the exact expiry boundary, fresh stock preparation or a due run appends one schema-2 BatchExpiryPending event. The quantity remains physical. FEFO and order evaluation exclude it. Gateway owns scheduled ticks; Warehouse starts no expiry timer. The selected legacy mode and replay of historical schema-1 BatchExpired events retain final-writeoff behavior.

POST writeoffs requires a currently authorized human with writeoff.confirm. Confirmation writes off the pending residual quantity once and records the original actor, time, unit and event ID. Its namespace is restaurant/epoch/command. Its frozen canonical fingerprint also binds the organization and batch. Exact retries return the original immutable result, including when another authorized human retries. Changed payload conflicts. A new command for a written-off batch returns AlreadyWrittenOff; an available batch returns NotPendingWriteoff. GET writeoffs/commands is passive. Unseen and pending observations retain unknown outcomes. A different epoch returns RuntimeReset before lookup. Serving order-command lookup does not expose confirmation records.

Stock preparation and calculation are separate typed ports. A committed pending transition sets X-Umir-Committed-Version, X-Umir-Invalidation-Required and X-Umir-Runtime-Epoch before calculation. These headers remain present if calculation fails, a later due transition fails, or confirmation is refused after preparation. ConfirmWriteoff returns a WriteoffPreparationError with the known committed preparation version and a wrapped refusal/error cause. The confirmation outcome remains independent; a refused command remains not_committed. Successful confirmation and exact retries retain the same immutable WriteoffResult. Stock rows distinguish available, pending and on-hand quantities. PrepareStock returns an isolated event-prefix projection; CalculateStock uses that captured projection.

Scoped delivery intake records DeliveryReceived with schema 2 and captured batch settings/tzdata. An already-expired intake records delivery and pending transitions in one atomic batch. Admission and projection use the same schema selector. Recovery preserves schema-1 delivery and final-expiry history, and accepts schema-2 delivery, pending and confirmation facts.

The bounded one-shot post-prepare calculation barrier is in tests/fixtures/protocol/analytics.go. It matches an operation and synthetic command ID, expires within 30 seconds and observes actual preparation. Production handlers contain no fixture flag, control route or fault. The W6 test wrapper integrates RPC controls and the same owner clock. W5 implements captured calendar, timezone retry and analytics contracts. The packaged data identity and captured batch settings are preserved. Complete real-service assembly and acceptance remain J1/E work.

Focused verification: GOWORK=off go test -race -count=1 ./internal/application ./tests/integration ./tests/fixtures/protocol -run TestW3. Run make check and make quality-test for complete module gates.

## W5 analytics and organization time

Warehouse exposes authenticated GET analytics/stock and analytics/movements under /internal/warehouse/v2/r/{restaurantID}. Each query requires expectedSettingsVersion. Auth JWT settings supply the organization zone and version. ProductID and NFC case-folded name search intersect. Limit defaults to 100 and has a maximum of 500. Duplicate, unknown or invalid query fields fail explicitly.

Stock reports available, pending, on-hand and expiring quantities per product/base unit. On-hand equals available plus pending. Expiring is a subset of available. RiskHours defaults to 48 physical hours and accepts 1 through 8784. The response preserves evaluatedAt, stateVersion, prefixSequence and the actual riskCutoff. Stock does not use a movement period. A preparation commit publishes invalidation headers before a later calculation error. CalculationUnavailable does not undo that commit.

Movements use committed Event.RecordedAt and actual supply, order-allocation and final-writeoff quantities. Pending transitions and rejected or unresolved attempts do not contribute. The reader validates one complete journal prefix independently of stock preparation. Unknown schema returns UnknownEventSchema; corrupt or duplicate facts fail unavailable. FromDate and inclusive throughDate must be supplied together. The default covers today and six earlier organization-local dates. The maximum is 366 local dates. Resolved from is included and to is excluded. Coverage identifies this restaurant runtime epoch and its actual registration start. A period before retained history returns complete=false and availableFrom; it does not claim earlier zero movements.

Explicit movement boundaries and default local dates use the same injected packaged timezone loader as delivery expiry. Host ZONEINFO does not override these rules. Projection synchronization validates the captured journal prefix before comparing the store version. If another Owner appends during recovery, synchronization retries at most three reads. Continued contention returns InventoryUnavailable with a concurrent-modification cause. Invalid journal facts fail without retry. Explicit Recover still fails closed when its captured journal has advanced; later reads reconcile the latest committed state.

Every cursor is a random opaque server handle. The retained immutable report binds actor, scope, epoch, lifecycle/settings, normalized filters, limit, period and risk horizon. Page identity, evaluation, resolved boundaries and cutoff remain equal across pages even after concurrent writes. Each runtime retains at most 128 paged reports for five owner-clock physical minutes. Capacity returns LimitExceeded. Unknown, expired, changed-scope/filter/settings or reset cursors return RefreshRequired with details.refreshRequired=true. Finished pages omit nextCursor. Runtime reset discards business facts and reports and changes coverage.

Scoped POST deliveries accepts the frozen TimeInput objects: an explicit RFC3339 instant or a civil localDateTime with an optional offset/occurrence. Instants need not use the organization offset. Manual folds require earlier/later or a matching offset; gaps reject. Day shelf life remains value times 24 physical hours. Month shelf life uses the captured organization calendar, clamps month end, chooses the earlier fold and resolves a gap to its first valid instant. Calendar date boundaries choose the first valid instant and earlier repeated midnight; a wholly skipped boundary date returns SkippedDate.

Delivery command fingerprinting retains the original form. Exact retained commands and unknown candidates resolve before a fresh stale-version check. Their receipt includes original batches, expiry, captured zone/settings/data and event context. A new stale form returns SettingsChanged. An admitted in-flight operation finishes with its captured zone. Replay and snapshots preserve these facts; they do not recompute expiry from new settings.

Public bootstrap Options.Now supplies authoritative admission, evaluation and report-retention time. Registry passes its captured coverage start through Options.CoverageFrom. Runtime.AnalyticsStockUsing accepts a consumer-owned stock port so test-owned post-prepare decorators can exercise the real preparation and calculation boundary. The ordinary AnalyticsStock path uses the real runtime. Test controls and barriers remain under tests. The test-owned W6 binding is documented in tests/fixtures/protocol/README.md. Full real-service assembly remains J1/E work.

Run go test -race -count=1 ./bootstrap ./internal/application ./internal/domain ./internal/adapters/zonedata ./tests/integration -run TestW5 for focused checks. Run make check and make quality-test for all module gates, then go mod verify and go mod tidy -diff. The Docker build copies go.mod and go.sum before dependency download and embeds the same zone archive in the static binary. Real Auth and peer assembly remain J1/E work.

## W6 handoff and operational fixtures

The dedicated test binding is described in [tests/fixtures/protocol/README.md](tests/fixtures/protocol/README.md). It composes only Warehouse public bootstrap/contract surfaces. Fixture binaries and fault controls are under tests and are absent from the production image. The selected legacy image and all published versions remain unchanged. Build the complete reviewed dirty candidate and retain its whole-module manifest; Git HEAD alone does not identify this candidate.

Run make check, make quality-test, go mod verify and go mod tidy -diff with GOWORK=off. The final quality suite verifies production import bans, fixture composition and peer-service import rejection. The task evidence records native/Linux binaries, the local test image, protocol/schema readbacks and exact predecessor/cumulative patches. Independent review and real J1/E service assembly remain required.

### Unpublished Warehouse owner profile

The isolated test wrapper supports `fixture-server serve-profile --profile warehouse-tzdata-missing`. Build `tests/fixtures/Dockerfile` target `tzdata-missing`. Its build stage replaces the embedded archive with the fixed zero-byte file in `tests/fixtures/profiles/data`. The candidate archive and production constructors remain unchanged. The default `runtime` target keeps the healthy archive.

Use the normal private owner config and a separate mode-0600 `/run/umir-fixtures/profile.json` with `amendment`, `project`, `profile`, and `profileContractIdentity`. The exact reviewed profile identity is `sha256:ee5ea052cf8239a40fdc3aba7c97c0a143260be284c390168a75c8afb7770460`. The supervisor starts its private listener before one real composition attempt. A missing archive refuses `bootstrap.NewRegistry` with `TimeZoneDataUnavailable`. It creates no business listener, handler, or worker. Its standard healthcheck fails. It retains the typed refusal for at most 300 monotonic seconds and shuts down within five seconds. It never reports ordinary readiness.

The separate POST `/owner-profile/v1` endpoint uses the existing Warehouse control credential. Write a mode-0600 request to `/run/umir-fixtures/profile-request.json`, then run `fixture-control profile --request /run/umir-fixtures/profile-request.json`. Each request binds the project, request ID, current instance and clock generation. Obtain the instance and generation with the existing read-only `clock.read`; refusal mode rejects every other normative operation. The sole refusal action is `report` with `profile=warehouse-tzdata-missing`. Healthy ordinary wrappers accept the standalone `catalog-report` action without a profile selector. Reports have an independent 64-entry exact replay cache; changed request reuse, stale instance/generation, foreign ownership and unsupported actions fail before observation. No RPC capability or schema changes are required.

The `catalog-report` envelope forbids the `profile` field, including empty and null values. The `report` envelope requires it. Invalid envelopes return `InvalidRequest` before catalogue observation or replay caching. A rejected envelope does not reserve its request ID.

Healthy catalogue reports compare the actual Warehouse runtime version with `zonedata.Inspect()` metadata and `/usr/local/lib/umir/warehouse-provenance.json`. The test build writes that fixed manifest from the actual archive, executable and pinned Go toolchain source. `TOOLCHAIN_IMAGE_ID` records the resolved build toolchain image identity; the runner must verify it against the actual build. The manifest digest appears in the report. Production exposes no profile endpoint.

The independent Feedback discovery overlay `b2e3b3824e622d68360d73854f24920c827b9c1aefadf013241ec69262daa02f` does not change Warehouse credential ownership. Warehouse still receives only `auth-warehouse`, `gateway-warehouse`, `serving-warehouse`, and its separate `fixture-warehouse` control credential. The base FOC binding remains `af215b378140b8e690523fc537007679b8c2599ed3039f7f2a0c651a64c59410`; protocol remains `umir-operational-fixtures/1.1.0`. Profile source/image smoke is an owner handoff, separate from runner qualification and J1/E acceptance.

## Durable storage foundation (wave 2, in progress)

The W2 branch includes the shared PostgreSQL, RabbitMQ, Redis and outbox relay adapters copied from Factory `reference/durable@26166c8ba197`. Production bootstrap still uses the W1 stores. The durable schema, journal, outbox composition, shared captures, CLI and readiness are follow-up work; this foundation does not make Warehouse durable.

Run `make check`, `make quality-test` and `make integration`. Integration tests need `UMIR_TEST_DATABASE_URL`, `UMIR_TEST_AMQP_URL`, `UMIR_TEST_AMQP_MANAGEMENT_URL` and `UMIR_TEST_REDIS_CACHE_URL`. In the Factory W0 checkout, run `bash scripts/durable-test-infra.sh up`, then evaluate its `env` output in the test shell without printing it. The suite fails when configuration is missing. It creates temporary `t_warehouse_*` databases and broker vhosts and a `t:warehouse:*` cache prefix, and removes only these test objects. The cache test does not use limiter Redis.

Check provenance with `bash /path/to/factory/reference/durable/copy.sh --check postgres rabbitmq redis relay /path/to/umir-warehouse`. Edit the Factory reference before refreshing a copied adapter. Business rules and application ports must not import these mechanisms.
