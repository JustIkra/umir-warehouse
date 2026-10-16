# Изменения

## Не выпущено

- Accept a repeated restaurant registration with different settings. For the same lifecycle version, a higher `settingsVersion` replaces the stored time zone and settings while the inventory stays, a lower one is a successful repeat that changes nothing, and the same version with another time zone is still a `CommandIdentityConflict`. A repeat of a lost registration answer after the organization's settings changed now succeeds.

- Keep the projection when a request ends early. The owner lock is a context-aware mutex: a request whose context ends while it waits leaves without touching anything, and `usable` is no longer cleared by a canceled or timed-out request, only by a journal that contradicts the projection. The server's `WriteTimeout` is now the request timeout plus five seconds so a result recorded just before the deadline can still be written.

- Answer commits and passive reads from bounded store reads. `order-commits` takes the position of the order from `FindOrderPlacement`, so a second command for a proposal that is already an order answers 200 with the same order instead of an unknown outcome; `orders`, `order-attempts`, `commands` and history pages use `Head`, `FindOrder` and `HistoryPage` instead of copying the journal. Planning holds the owner only to capture the version and a shallow view and reads the period facts without it; `Movements` reads the events of its period and takes names from the captured products. `Analytics` reads the journal through `CaptureJournal` and `ReadPeriod`, which replace `ReadAnalyticsJournal`. `bootstrap.Options.WrapEventStore` lets tests wrap each restaurant's store.

- Keep snapshot and journal faults apart from store failures. A snapshot that the journal contradicts is logged and dropped, the worker goes on and the position is not verified again. Recovery falls back to the whole journal only for faults in the snapshot or the journal, never for a store error, and marks the projection unusable when the journal is inconsistent. Snapshot validation also requires a version that covers its records and delivery lines numbered 1..n. `Owner.ConfigureLogger` injects the logger.

- Index batches by product so evaluating an order reads only the batches of its ingredients that can still supply them.

- Add `make perf`, a scale harness under `tests/perf` (build tag `perf`, linted through `run.build-tags`). It builds a restaurant with 1000, 4000 and 8000 orders over the memory stores and prints latencies of commits, menu, supply, recommendations, inventory, history, movements and recovery (live, from the journal, from a snapshot), plus the heap of a freshly restored owner. It checks only growth ratios between the first and last checkpoint.

- Stop copying the projection on every read. `Owner.Prepare` returns an independent copy of products, dishes (with their ingredients) and batches only, without the delivery IDs, and the owner's own commands work on a borrowed view of its state under the lock. Offering a snapshot clones the state once. Writes to the maps returned by `Prepare` can no longer reach the owner or a snapshot.

- Verify a snapshot against the journal once, when it is saved. Outside the owner lock, the snapshot worker replays the journal from the previous valid snapshot (or from the start when there is none) up to the candidate's sequence and saves the candidate only if it holds exactly that state; a diverging candidate is rejected with `ErrInvalidSnapshot`, dropped without retrying, and leaves the previous snapshot in place. If the previous snapshot itself contradicts the journal, the candidate is judged from the start so one wrong snapshot cannot block later ones.

- Recover the projection from a streamed journal. The owner builds the candidate in local variables from the latest snapshot plus `ScanBatches` of the tail, so a start from a snapshot no longer reads the journal prefix or holds per-journal ID maps. A snapshot that is invalid or does not fit the tail is logged and replaced by a walk from the start. The candidate is published only after the whole rebuild and the store version agree, so a failed or cancelled recovery keeps the previous projection usable, and catching up on the journal tail applies batches one by one and no longer marks the projection unusable on a cancelled context. The legacy full history keeps walking the journal from the start.

- Apply recorded batches through a change layer: `PlanBatch` validates against the state plus the earlier events of the same batch and returns a small `StateDelta`, `Merge` writes it in, and `ApplyBatch` does both. A rejected batch leaves the state untouched and applying one batch no longer copies the state or depends on the order history. The owner updates the expiration index in place and checks order and command identity through the event store instead of its state; the duplicate command, order and proposal checks leave the reducer because the store already enforces them.

- **BREAKING:** Drop order and command history from `InventoryState`, which now holds products, dishes, delivery IDs and batches. Snapshots use the new `SnapshotSchemaV2` and no longer carry orders, commands or delivery lines; the structural check covers dishes, batches (balance, times, product, delivery) and delivery IDs, and recovery applies the journal tail on top of a snapshot, falling back to a full replay when it does not fit. The old snapshot schema is rejected with `ErrUnsupportedSchema`. Snapshots were only kept in memory, so there is nothing to migrate: after a restart the owner replays the journal.

- Add bounded reads to the EventStore port and the memory store: `ScanBatches` for streaming, `ReadEvents` for a window of events, `ReadPeriod` for events of a time range up to a captured sequence, and `FindOrderPlacement` for the sequence and time of an order's `OrderPlaced` event. Cover them in the shared store contract suite. The memory store now finds batch boundaries by binary search.

- Reject duplicate order and delivery IDs in the memory event store with `ErrInvalidEvent`, as it already does for proposal IDs, so the store keeps these identities unique without relying on the reducer.

- Add a shared EventStore and SnapshotStore contract suite in `tests/storecontract` and run it against the memory stores. It replaces the earlier per-store contract tests and is reusable by the PostgreSQL stores.

- Add FIN1 read-only supply suggestions and coherent private recommendation inputs. Use exact recorded consumption, eligible current stock, fixed seven-day history, three-day threshold and seven-day target, with explicit partial-history coverage. Keep ranking in Serving and require its narrow recommendation permission. Preserve human writeoff semantics and C2/UA1 source artifacts; assembly integration remains separate.

- Add UA1 scoped product-batch pages with positive available/pending balances, owner FEFO ordering, atomic state/prefix capture and bounded actor-bound cursors. Preserve per-batch expiry/settings and human writeoff confirmation. Keep original C2 schemas and selected assembly unchanged.

- Repair W6 scoped order commits, order-ID lookup, command-only retained attempts, bounded journal history and prepared expiring-batch dispatch against the frozen C2 contract. Add authenticated success, replay, isolation and schema regressions.
- Separate failed JWKS retry timing from authoritative key/JWT time. Recover readiness at a fixed fixture instant without stale-key fallback; preserve immediate owner-time cache expiry refresh.

- Add the W6 test-owned Warehouse server, Unix helper and separate image for operational protocol 1.1.0. Bind one controlled clock to real registry/store, JWT verification and Auth key discovery. Preserve fresh epochs and empty business state on restart.
- Add bounded owner fault decorators, actual post-commit reply loss and retained-result checks. Consolidate the earlier analytics fault manager and retain committed invalidation after calculation failure.
- Permit fixture composition while retaining production import bans and rejecting fixture peer imports. Add enforcement regressions and document explicit legacy/v2 migration. Published versions and selected pins remain unchanged; independent review and real J1/E acceptance remain separate.

- Fix passive ID suggestions retrying unresolved owner commands. Return inventory-unavailable until the original command resolves; preserve its exact retry and retained result.

- Add W4 passive tenant-local product/dish ID suggestions through the typed bootstrap port and frozen authenticated private route. Use canonical positive decimal strings without fixed-width conversion; preserve exact manual IDs, atomic creation conflicts and retained command results.

- Repair scoped delivery schema-2 admission/replay and preserve committed preparation invalidation on partial due-run failures and refused writeoff commands. Keep exact confirmation retries immutable.

- Add scoped schema-2 pending expiry and idempotent human confirmation with passive epoch-bound lookup. Preserve physical stock, FEFO exclusion and historical final-expiry replay.
- Publish committed preparation invalidation before calculation and add the bounded test-only analytics barrier. W5 adds calendar/analytics; W6 integrates the executable fixture.

- Add W2 Auth-only epoch/generation registration and close acknowledgments, authenticated runtime discovery and current-history coverage.
- Fence retained Warehouse handles at close acceptance; drain admitted work, cancel/join snapshot workers and retry partial cleanup without reopening the restaurant.
- Preserve process-local deletion markers, neighboring restaurant state and unknown prior-epoch outcomes. Auth retains durable lifecycle authority.

- Add the W1 restaurant registry with isolated owners, journals, snapshots and attempt coordinators.
- Add an opt-in v2 private request boundary with Auth-only Ed25519 verification, per-caller transport keys, operation/capability checks and tenant-bound passive lookups.
- Classify v2 order-evaluation boundary failures as read failures (`unavailable`) despite POST transport; preserve mutation refusals and runtime-reset uncertainty.
- Resolve known v2 operations before refusing malformed restaurant IDs so POST order evaluation returns `unavailable`; retain refusal before trust/runtime work and the unsupported-route fallback.
- BREAKING in v2 mode: public business and unscoped routes refuse dispatch. Legacy mode remains separately selectable. Auth owns persistent lifecycle reconciliation. W1–W6 implement the scoped Warehouse surface; real assembly compatibility remains J1/E work.

- Обновлены закреплённые GitHub Actions до выпусков с поддержкой Node.js 24.
- Обновлены минимальная версия Go и builder image до 1.26.9 для получения актуальных исправлений безопасности стандартной библиотеки.
- Добавлен HTTP v1 warehouse: пять публичных операций и одиннадцать внутренних маршрутов с раздельными listeners, строгими DTO и ограниченным временем/размером запросов.
- Добавлены process-local состояния попыток оформления: резервирование до Owner, сохранённые результаты/отказы, `Pending` при неизвестном исходе и пассивный lookup.
- `bootstrap.NewHTTPHandlers` собирает публичный и внутренний обработчики на одном Runtime; `cmd/server` восстанавливает Runtime до старта обоих listeners и корректно останавливает их вместе с workers.
- Добавлены публичная сборка Runtime, справочники и приём поставок с атомарным приходом и списанием уже просроченных партий.
- Добавлен публичный `POST /expiration-runs`, который возвращает время, версию и результаты из одной атомарной обработки просрочки. Warehouse больше не запускает отдельный периодический обработчик истечения.
- Добавлена настройка `SNAPSHOT_RETRY_INTERVAL` для задержки перед повтором фонового SnapshotWorker; `EXPIRATION_CHECK_INTERVAL` больше не читается.
- Lookup записанных результатов не требует текущей проекции; отпечаток ConfirmOrder использует общий канонический формат.
- Dockerfile включает production-пакеты; добавлены закреплённые build/lint/architecture/race gates.
- Добавлены независимые оценки меню, упорядоченная оценка и атомарное оформление заказа с повторной проверкой остатков.
- Добавлены чтения остатков, партий к истечению и полной истории событий.
- Повторный commit возвращает существующий заказ после разрешения ожидающей записи или синхронизации проекции.
- Добавлено настраиваемое до восстановления фоновое сохранение полных снапшотов с ограниченной очередью, повтором ошибок и восстановлением по проверенному префиксу журнала.
- Повтор после ошибки фонового сохранения сохраняет кандидата; восстановление принимает снапшот только после replay-прослеживания его префикса тем же reducer.
- Сбой Save снапшота в cmd/server логируется и повторяется после `SNAPSHOT_RETRY_INTERVAL`; он не останавливает HTTP. Неожиданные ошибки lifecycle по-прежнему останавливают процесс.
- Приём поставки проверяет сумму живых остатков на полной версии команды и возвращает `internal/domain.ErrQuantityOverflow` без фактов поставки, если итог не помещается в int64. Немедленные списания входят в эту проверку.
- Изменён результат `GetDish`: `Runtime.GetDish` возвращает `contract.DishDetails` с `EvaluatedAt`, `StateVersion` и вложенным `Dish`. Go-клиентам нужно перенести чтение определения в поле `Dish`.
- Проверки качества явно отклоняют неподдерживаемые вложенные модули и поддерживают сборку библиотечных модулей без исполняемых файлов в исходном дереве. Ограничения часов в ядре также покрывают `time.Since` и `time.Until`.
- Проверки health сохраняют ошибки закрытия ответа; ошибки кодирования ответа health обрабатываются явно.
- Добавлены правила разработки Go, архитектуры и Git/release с закреплёнными инструментами качества.

## [0.1.0] - 2026-10-10

- Добавлены запускаемый каркас, Dockerfile и проверки GitHub Actions.
- Добавлена публикация образов из main по SHA коммита.
