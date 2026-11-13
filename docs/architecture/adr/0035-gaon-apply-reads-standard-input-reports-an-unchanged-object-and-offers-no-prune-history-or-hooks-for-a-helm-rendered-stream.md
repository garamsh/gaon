# ADR 0035: gaon apply reads standard input, reports an unchanged object and offers no prune, history or hooks for a Helm-rendered stream

> Status: accepted
> Date: 2026-11-13

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Issue #201. A user who keeps manifests as a Helm chart runs `helm template RELEASE CHART | gaon apply -f -`. Before this decision `apply` read only a file, and applying the same documents again printed `configured` for each, because a server write always bumps `resourceVersion` ([resource-api.md](../resource-api.md) §Common rules), so a rerun could not be told from a change.

Helm's own release management gives three things `helm template` does not: pruning an object a later render no longer holds, a history to roll back through, and hooks.

## Decision

**`apply -f -` reads standard input, `apply` prints `unchanged` and sends nothing for an object the document already matches, and gaon offers no prune, history or hooks for a rendered stream.**

1. **Standard input.** `-f -` reads the documents from standard input. The rules for a file apply to it unchanged: strict decoding, validation of every document before the first is sent, file order, a stop at the first failure.
2. **Unchanged.** For a document without a `resourceVersion`, `apply` compares the writable fields of the object it read with the document, leaving out `status` and the metadata the server sets, and sends no write when they are equal. A document that carries a `resourceVersion` is always written. No write is sent on a match, so compare-and-swap is not weakened: nothing is overwritten.
3. **Order.** The stream order is the order of `helm template`: Helm's install order for the kinds it knows, then template file-name order. `apply` does not reorder or retry, because the server does not require a referent to exist when an object is created. A rerun after a failure is safe, as finished objects print `unchanged`.
4. **Not offered.**
   - **Prune:** `apply` stores no owner or label for a render, so it cannot tell an object the chart made from another client's. Removal is `gaon delete KIND NAME`.
   - **History and rollback:** gaon keeps no record of a render. A rollback is applying an earlier render.
   - **Hooks:** `helm template` does not run them. A hook-annotated template is applied as an ordinary document.
5. **Kinds reached are unchanged.** The CLI serves the eight built-in kinds and the registered kinds, so a `Secret`, `ConfigMap`, `ServiceAccount`, `Service`, `Endpoints` or `NetworkPolicy` in a chart is refused. Adding them is a separate decision.

## Alternatives rejected

- **A no-op write on the server,** answering an equal PUT without bumping the version. It needs a change in `apiserver/` and in the rule that every write bumps the version.
- **A last-applied annotation or hash on each object,** compared on the next run. It changes stored objects, and it is also the record a prune would need, which is the part rejected.
- **Pruning by a release label,** deleting what carries the label and is not in the stream. A delete based on a label can remove an object another client labelled, and it is a second write path with no compare-and-swap of its own.
- **A retry of a document that failed on a missing referent.** The server has no such failure on create, so it would only hide a mistake in the chart.

## Consequences

- **A rerun of a stream prints `unchanged` for every object it already holds,** so a run shows what it did.
- **An object the server has added a field to differs from the document** and is sent: a MicroVM or Volume bound to a node (`spec.nodeName`) is refused by the server on re-apply, as before this decision. A chart that emits those kinds applies once.
- **A chart's removed object stays** until it is deleted by hand.
- **`Run` in `gaon-cli/internal/command` takes the standard input,** and the binary passes `os.Stdin`.

## Signs the decision was wrong

- Users keep a hand-made list of objects to delete after each chart change, and ask for a prune.
- A server default or defaulting webhook makes a stored object differ from its document, so a rerun prints `configured` for objects nobody changed.
- Users apply charts that need a kind the CLI does not reach, and work around it outside gaon.
