# ADR 0015: The gaon CLI derives the other groups' roots from `--server` and finds a registered kind's plural in the Established definitions

> Status: accepted
> Date: 2026-11-12

Drafted for garamsh/gaon#84. It builds on [ADR 0014](./0014-the-gaon-cli-reads-like-kubectl-get-lists-and-reads-describe-shows-a-kind-has-a-plural-and-a-short-name-and-list-is-removed.md), whose grammar and kind-name rules it keeps.

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

- **`--server` is the compute group's root in full** ([cli.md](../cli.md) §Commands), `http://<host>:<port>/apis/compute.gaon.garam.sh/v1alpha1`. The new kinds are served at other roots: ClusterRole and ClusterRoleBinding in the RBAC group, CustomResourceDefinition in the apiextensions group, and the objects of a registered kind in the group the definition names. Every `lib/api/` client is rooted at one group.
- **A registered kind's plural is not known to the CLI.** [resource-api.md](../resource-api.md) §CustomResourceDefinition gives the objects of a definition the path `/apis/<group>/<version>/<plural>`, and the plural is a field of the definition. `lib/api/` has `NewCustomResourceClient` for a type given its group, version, kind and plural, and `CustomObject.Validate` for the identity of an object; neither finds a definition.
- **A definition has no short name.** Its names are `kind`, `listKind`, `plural` and `singular`, so a registered kind cannot take a short name ADR 0014 §4 asks of each kind.
- **A definition serves its objects only once Established**, which the API server records as a condition.
- **ADR 0014 left the grammar to this issue.** Its Consequences say a kind is added as a `spec` entry with its three names, which holds for the four built-in kinds added here and not for a registered kind.

## Decision

**The roots of the other groups are the `--server` URL with its compute suffix replaced; a registered kind is found among the Established definitions the API server serves, and is matched after the built-in kinds.**

1. **Roots.** The CLI removes the suffix `/apis/compute.gaon.garam.sh/v1alpha1`, and a trailing slash, from `--server`, and appends `/apis/<group>/v1alpha1` for the RBAC and apiextensions groups, and `/apis/<group>/<version>` for a registered kind. A `--server` that does not end in that suffix still serves the compute kinds. Any command that needs another group's root is a usage error naming the expected form, before a request to that group is sent. There is no second flag.
2. **Built-in kinds.** ClusterRole (`clusterrole`, `clusterroles`, `cr`), ClusterRoleBinding (`clusterrolebinding`, `clusterrolebindings`, `crb`), CustomResourceDefinition (`customresourcedefinition`, `customresourcedefinitions`, `crd`) and CertificateSigningRequest (`certificatesigningrequest`, `certificatesigningrequests`, `csr`). No short name is used by any other kind. Each verb is where §Paths has a path: ClusterRole, ClusterRoleBinding and CustomResourceDefinition take `get`, `describe`, `delete` and `apply`; CertificateSigningRequest takes `get`, `describe` and `apply` (its create), and `delete` is refused because §Paths has no `DELETE`. The CLI never writes `/status`.
3. **Registered kinds.** When an operand names no built-in kind, the CLI lists the CustomResourceDefinitions once per command and keeps those with a true `Established` condition. An operand matches a definition by its `singular`, its `plural`, or its `metadata.name` (`<plural>.<group>`), without regard to case. A definition has no short name, and none is invented. A server that has no definitions path counts as having none.
4. **Precedence.** A built-in name wins over a registered one. The definition's `metadata.name` always reaches its own kind. An operand that matches more than one definition is a usage error that names each as `<plural>.<group>`; the CLI does not choose.
5. **Unknown kind.** The usage error lists the built-in kinds and the Established registered kinds. A definition not yet Established is not listed and not matched.
6. **Apply.** A document is matched by its `apiVersion` and `kind`: a built-in kind by its group and version, a registered kind by the group, the one version and the `kind` of an Established definition. The CLI decodes a registered object strictly into `CustomObject` and checks it with `CustomObject.Validate` for the definition's type before any document is sent. The schema is checked by the API server, as the CLI does not hold it.
7. **Output.** A registered kind's table is `NAME GENERATION`, and `describe` adds `Spec` and `Status`, each as one line of compact JSON, or `-` when absent. `-o yaml` and `-o json` print the object as the API returned it.
8. **What "nothing is sent" means.** ADR 0014 and [cli.md](../cli.md) §Apply said a refused apply sends nothing. The definitions list is a read made to find a registered kind, so an apply that is refused may have sent that one `GET` and sends no write.

## Consequences

- **A registered kind works the day its definition is Established, with no CLI release.**
- **A kind's resolution costs one `GET` of the definitions per command** that does not name a built-in kind, and none for a built-in kind.
- **`--server` stays the only address.** A deployment that serves the groups behind different roots, or a `--server` that is not the compute root, reaches only the compute kinds.
- **A definition that shadows a built-in name is reached by its `<plural>.<group>`.** `get machines` is always the built-in Machine.
- **A newer definition with the same plural in another group makes `get <plural>` ambiguous** until the user qualifies it; an earlier script that used the bare plural stops working, and says why.
- **The table of a registered kind has two columns.** The CLI holds no schema, so it prints nothing of `spec` in a table; `-o yaml`, `-o json` and `describe` do.
- **Rejected: a flag or an environment variable per group root.** Three addresses for one API server is a setup error waiting to happen, and the suffix is fixed by §Paths.
- **Rejected: discovering the roots from the API server.** Neither the contract nor `lib/api/` has a discovery path, and one would be a new interface for a derivation the paths already give.
- **Rejected: a registered kind taking the first letters of its singular as a short name.** It would collide with a built-in or another definition with no rule to settle it, and a published short name cannot be taken back, as ADR 0014 says.
- **Rejected: choosing the oldest or newest of several matching definitions.** The answer would change as definitions are created, and a `delete` run under it could reach the wrong kind.
- **Rejected: listing every definition, Established or not.** An object of a kind that is not Established has no path to be read from, so the command would fail with a not-found that hides the cause.

## Signs the decision was wrong

- Users need a group root that is not `--server` with its compute suffix replaced, and ask for a flag.
- The definitions list is large or slow enough that a command's cost is its lookup.
- Definitions that collide on a singular or plural are common, and `get` of the bare name is asked to pick one.
- A registered kind gains a short name in the definition, which this ADR does not read.
