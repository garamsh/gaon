# ADR 0031: A schema node may hold an integer or a string with x-kubernetes-int-or-string and no type

> Status: accepted
> Date: 2026-11-13

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

Goal #117, decision D8, found at the review of PR #176 (issue #178). The schema subset of [ADR 0009](./0009-a-user-registers-a-resource-kind-with-a-customresourcedefinition-the-api-server-serves-it-under-its-own-group.md), Decision 5, has no node that holds an integer or a string. The Deployment of [ADR 0026](./0026-a-deployment-is-a-kind-the-workloads-unit-keeps-replicas-of-a-microvm-template-for-with-no-replicaset-kind-and-a-rollout-bounded-by-surge-and-unavailability.md) therefore takes `maxSurge` and `maxUnavailable` as strings that match a pattern, and refuses `maxSurge: 1`, which Kubernetes accepts. ADR 0009's Consequences say a user who needs a keyword outside the set waits for a later ADR.

Kubernetes' structural schema marks such a node `x-kubernetes-int-or-string: true`. Kubernetes requires the node to omit `type`, or to give an `anyOf` of an integer and a string. ADR 0009 has no `anyOf`.

## Decision

**A schema node with `x-kubernetes-int-or-string: true` and no `type` holds an integer or a string. This amends ADR 0009, Decision 5.**

1. **Keyword.** `x-kubernetes-int-or-string` joins the keyword list. The node has no `type`: the flag with a `type` is `Invalid`, and the `anyOf` form is not taken because `anyOf` stays outside the subset.
2. **Other keywords on such a node.**
   - `pattern` applies to the string branch and must compile.
   - `enum` entries may be integers or strings; any other entry is `Invalid`.
   - `minimum` and `maximum` are `Invalid`.
   - `properties`, `required`, `items` and `x-kubernetes-preserve-unknown-fields` are `Invalid`.
3. **Values.** A write passes the node when the value is an integer or a string. A string must match `pattern` where there is one, and a value, integer or string, must be one of `enum` where there is one. Anything else, a boolean, a fraction, an array or an object among them, is `Invalid`. `null` is an absent field, as for any node.

## Consequences

- **A Kubernetes reader's `maxSurge: 1` is accepted** once the Deployment's schema uses the flag. That change is a workloads issue and is not part of this decision.
- **Interpreting a string is the reader's job.** The schema checks that `"25%"` matches a pattern; the controller that reads it decides what percent of what it means.
- **`minimum` and `maximum` are refused rather than applied to the integer branch,** so a bound on the integer needs a later decision and no bound is silently unchecked.
- **The keyword list in the responsibility document changes with this ADR,** and ADR 0009's list is no longer the whole list.

## Signs the decision was wrong

- Users need a bound on the integer branch and write a pattern on the string to approximate it.
- Users ask for `anyOf` for other unions, and the flag is one special case among several.
