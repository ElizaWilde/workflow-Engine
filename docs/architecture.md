# Architecture decisions

The API depends on a workflow service, which depends on a storage interface and domain types. Memory and Oracle implement the same storage port. `cmd/api` chooses the implementation and owns resource cleanup.

Quotes are immutable snapshots between transitions. Each successful command creates one audit event at the same version as the persisted quote. The storage boundary owns atomicity and compare-and-swap, so workflow rules do not depend on a database driver.

The initial scope supports quote creation and state transitions. Quote revisions, searches, workflow definition persistence, asynchronous execution, and BMI transport integration are future extensions. No BMI endpoint or credential contract is assumed.

Before implementing an external mutation, decide how its intent is persisted alongside the quote. An outbox and an idempotent worker are the recommended extension; a SQL transaction cannot atomically commit an arbitrary remote HTTP request. Document retry, timeout, and compensation behavior with that implementation.
