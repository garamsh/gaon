# ADR 0014: The gaon CLI reads like kubectl: `get` lists and reads, `describe` shows, a kind has a plural and a short name, and `list` is removed

> Status: accepted
> Date: 2026-11-12

Drafted for garamsh/gaon#100. The goal is the owner's, stated in that issue: someone who has used `kubectl get` uses `gaon` without relearning it. No Kubernetes documentation was read for this record; what it says of `kubectl` is what #100 says, and the choices below are this project's.

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

- **What differed at garamsh/gaon commit `d40dced208b84c2ac50eb8cd90241cf467e973e3`**, from #100 and `gaon-cli/internal/command/command.go` there:
  - reading one and listing were two verbs, `get KIND NAME` and `list KIND`, and `get KIND` was a usage error;
  - a kind was accepted by its singular only, so `get nodes` was an unknown kind;
  - `get` printed YAML by default and `list` a table;
  - the flag set stopped at the first operand, so `get machine web -o yaml` failed;
  - Volume had every verb refused. That was right when [resource-api.md](../resource-api.md) §Paths held only `PUT /volumes/{name}/status`; it no longer is, since `PUT`, `DELETE`, `GET` of one and `GET /volumes` are there ([ADR 0013](./0013-a-volume-is-created-through-its-own-path-a-local-volume-is-a-disk-its-nodes-agent-makes-and-a-binding-pins-it-to-a-node.md));
  - there was no `describe`.
- **The constraint on verbs stands.** [cli.md](../cli.md) §Verbs by kind: a verb works for a kind where §Paths has a path for it, and the CLI adds none the contract lacks. Nothing here changes it; the Volume row changes because §Paths did.
- **`lib/api/` has a client method for every Volume path** (`PutVolume`, `DeleteVolume`, `GetVolume`, `ListVolumes`), so no client change is needed.

## Decision

**`get KIND` lists, `get KIND NAME` reads one, `describe KIND NAME` shows one, the `list` verb is removed, and a kind is named by its singular, its plural or a short name.**

1. **`get`.** One operand lists the kind, two read one. More than two, or none, is a usage error. The default output is a table; `-o yaml` and `-o json` print the object, or the list object when listing, as the API returned it. Any other `-o` is a usage error before a request is sent.
2. **`list` is removed.** `gaon list` is an unknown command and exits 2. It is not kept as an alias.
3. **Flags among operands.** A command's flags are read wherever they stand among its operands. A `--` ends the flags. An unknown flag is a usage error wherever it stands. The global flags stay before the command.
4. **Kind names.** A kind is matched, without regard to case, by one of three names, and no two kinds share one:

   | Kind | Singular | Plural | Short |
   |---|---|---|---|
   | Machine | `machine` | `machines` | `mc` |
   | Node | `node` | `nodes` | `no` |
   | Lease | `lease` | `leases` | `le` |
   | Volume | `volume` | `volumes` | `vol` |

   The short names are this ADR's choice; none is taken from `kubectl`. An unknown kind is a usage error listing every kind with its other names.
5. **`describe`.** It reads one object and prints its metadata lines, the kind's own lines and, for a Machine and a Node, a table of its status conditions. A Lease and a Volume have no conditions in the contract and print none. It takes no flag.
6. **Volume.** `get`, `describe`, `delete` and `apply` work on it, each on its path in §Paths. A Volume's table is `NAME CLASS SIZE PHASE NODE`.

## Consequences

- **A `kubectl` user's `get nodes`, `get mc web -o yaml` and `describe node n1` work as typed.** Output is a table unless asked, so a script that read the YAML of `gaon get KIND NAME` without `-o` now gets a table.
- **A script using `gaon list` breaks.** Searched on 2026-11-12 at commit `d40dced208b84c2ac50eb8cd90241cf467e973e3`: `git grep -nE 'gaon (--[a-z-]+ [^ ]+ )*(list|get)\b|gaon list' d40dced -- . ':!gaon-cli/internal'` matched only lines of `docs/architecture/cli.md`, which this change rewrites, and `git grep -nE '"list"|gaon[^a-z-]+list' d40dced -- tooling .github Makefile` matched nothing. The CLI's tests in `gaon-cli/cmd/gaon` pass `list` as an argument and are changed here. A script outside this repository is not covered.
- **The kinds the CLI reaches are still four.** Adding one is a `spec` entry in `gaon-cli/internal/command/kinds.go` with its three names; #84 does so for RBAC and resource definitions, each with a singular, a plural and a short name no other kind uses.
- **A short name is a promise.** Once published it is kept, or a new ADR supersedes this one.
- **`describe` prints no events and no related objects.** The contract has no event resource, and a Machine's Volumes are named in its spec and not fetched.
- **Rejected: keeping `list` as an alias of `get KIND`.** It would be a second spelling of the read this issue makes `get`, documented and tested for nobody who has used `kubectl`, and removing it later would break the callers the alias had gathered. The CLI is `v1alpha1` and the only callers in this repository are changed in the same pull request, so the break is cheapest now.
- **Rejected: keeping YAML as the default of `get KIND NAME`.** One default for `get` is what a user expects from `get KIND`; two defaults depending on the operand count is the difference being removed.
- **Rejected: a short name taken from the first letters of the kind (`m`, `n`, `l`, `v`).** One letter is likely to collide with the next kind added, and renaming a published short name is a break.
- **Rejected: flags parsed by a library.** The stdlib `flag` package, run again after each operand, gives the grammar needed with no new module in `gaon-cli/go.mod`.

## Signs the decision was wrong

- Users or scripts keep typing `gaon list`, and an alias is asked for in issues.
- A kind is added whose natural short name is taken, and it is given an unrelated one.
- Scripts parse the table, which carries no stability promise, because `-o json` lacks a field they need.
- `describe` is asked for events or related objects the contract cannot supply.
