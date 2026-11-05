# Architecture decisions

The GraphQL (gqlgen) and REST transports call the same workflow service, `workflow.Engine`. It owns quote creation and draft updates, resolves actions to transitions, evaluates rules, and checks the authenticated actor's role and ownership. `cmd/api` configures bearer-token identities, selects PostgreSQL or memory storage, applies migrations, runs the outbox worker, and owns graceful shutdown and resource cleanup.

## Commands and permissions

The workflow definition is an explicit action-to-state map in Go. `Execute` resolves submit, approve, reject, issue, and cancel; `Move` maps the original target-state transport onto those same actions. `Update` replaces editable fields only while a quote is in draft. Rejected, cancelled, and issued states are terminal.

The MVP assumes a positive submission price, one approval stage for every quote, and separation between the owner and approving/rejecting actor. Sales users own the quotes they create; approvers read all quotes and decide submitted ones; admins may manage all quotes but cannot approve their own. These are local learning policies. Tokens establish identities on the server, and authorization also runs inside the service so another transport cannot bypass it. Actors are never accepted from command inputs.

Custom `Step` functions add transition rules before requests are served. They operate on immutable quote snapshots and must not perform external writes because persistence can still reject a command after its rules run.

## Transaction boundary

The workflow service depends on a repository port. Both memory and pgx/PostgreSQL implementations atomically commit three records: the next quote snapshot, one audit event containing that snapshot, and an outbox message. PostgreSQL uses a transaction, while memory uses one mutex. Every accepted command advances the version exactly once. An update with a stale expected version fails without an audit or outbox write.

The initial embedded migration creates quotes, quote_events, and outbox. Startup takes a PostgreSQL advisory transaction lock, checks schema_migrations, and applies the schema exactly once. The old Oracle driver placeholder and Oracle DDL have been replaced to match the README's PostgreSQL stack. This is a fresh PostgreSQL schema; importing an existing Oracle database is outside the MVP.

## Delivery boundary

A worker claims due entries using an atomic update and `FOR UPDATE SKIP LOCKED`. Each claim increments the attempt counter and sets a unique lease token and a 30 second lease. Acknowledgements and retries compare the token so an older worker cannot overwrite a newer worker's claim. An abandoned claim becomes eligible after expiry. Multiple API instances may process different events concurrently.

The worker invokes its handler outside any database transaction with a 10 second timeout. The default handler logs event metadata; an optional HTTP webhook receives the full event and a stable idempotency key derived from quote ID and version. Successful delivery is acknowledged separately. If acknowledgement fails after remote success, the event may be delivered again: receivers must deduplicate it. Ordering across retries/workers is not guaranteed. Consumers can compare versions or use the complete event snapshot.

Failures schedule exponential retries. A fifth failed attempt leaves a dead entry and diagnostic text for operator review and manual requeue. External failures do not roll back quote transactions. There are no compensating state transitions in this MVP because all external work is asynchronous notification delivery.

## Transport and operations

The GraphQL schema is checked in together with generated server code and its configuration. Money and versions use a decimal-string Int64 scalar to preserve the entire signed 64-bit range in JavaScript. The REST adapter continues to use JSON integer values. Both transports enforce a body limit and translate domain failures without exposing database errors. GraphQL also limits parser tokens and query complexity.

Liveness is independent of PostgreSQL. Readiness pings the configured pool. During shutdown the HTTP server drains requests, then the worker stops before the pool closes. Unacknowledged messages remain recoverable through their leases.

Integration tests connect to native PostgreSQL using the required TEST_DATABASE_URL. Each run creates and cleans up a randomly named schema so application tables remain untouched. They exercise the real PostgreSQL schema, atomic rollback, persistence across fresh connections, concurrent version checks, exclusive claims, lease fencing, and dead letters. The setup script installs a native PostgreSQL service and C compiler in WSL and CI. Unit and API tests cover rules, permissions, history snapshots, exact integer serialization, and webhook retries.

Redis, messaging brokers, gRPC, quote searches, line items, dynamic workflow definitions, and real Oracle/BMI endpoints are future extensions. No Oracle credential or endpoint contract is assumed.
