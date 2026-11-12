# ADR 0016: The gaon CLI reads its server and credentials from a config file of clusters, users and contexts

> Status: accepted
> Date: 2026-11-12

Drafted for garamsh/gaon#101. The goal is the owner's, stated in that issue: the CLI reads its server, credentials and current context from a config file, as `kubectl` reads a kubeconfig. No Kubernetes documentation or source was read for this record, so nothing below is claimed of `kubectl` or of a kubeconfig; the names `clusters`, `users`, `contexts` and `current-context` are the issue's, and every other spelling and rule is this project's choice. It builds on [ADR 0005](./0005-the-api-server-serves-beyond-loopback-only-with-mutual-tls.md), whose three client files it names, and on [ADR 0015](./0015-the-gaon-cli-derives-the-other-groups-roots-from-server-and-finds-a-registered-kinds-plural-in-the-established-definitions.md), whose `--server` it keeps.

Append-only: once merged, the body below is not rewritten. A fact later found wrong is corrected in an appended note, not edited out; a revised decision is a new ADR that supersedes this one.

## Context

- **At garamsh/gaon commit `a816336485420c429e9cf14356ce05f903453fd8`** only `--server` had an environment variable, `GAON_SERVER`. `--tls-ca`, `--tls-cert` and `--tls-key` were flags only, so a call against a mutual TLS server named three paths ([cli.md](../cli.md) §Commands).
- **The three files are one identity.** `lib/api/` builds a mutual TLS 1.3 client from all three or a plain client from none, and refuses one or two ([ADR 0005](./0005-the-api-server-serves-beyond-loopback-only-with-mutual-tls.md) §3). The CLI has no bearer-token path.
- **`--server` is the compute group's root in full**, and every other group's root is derived from it ([ADR 0015](./0015-the-gaon-cli-derives-the-other-groups-roots-from-server-and-finds-a-registered-kinds-plural-in-the-established-definitions.md) §1). A cluster entry in a file has to say which address it holds.
- **The CLI has no command that writes a file today**, and the config file holds paths, not secrets.

## Decision

**The CLI reads a YAML file of clusters, users and contexts with a current context; a flag beats the environment, which beats the file, field by field.**

1. **Format.** One YAML document, decoded strictly, so a field the format does not have is an error:

   ```yaml
   current-context: dev
   clusters:
   - name: dev
     cluster:
       server: https://10.0.0.1:8443/apis/compute.gaon.garam.sh/v1alpha1
       certificate-authority: ca.pem
   users:
   - name: admin
     user:
       client-certificate: admin.pem
       client-key: admin-key.pem
   contexts:
   - name: dev
     context:
       cluster: dev
       user: admin
   ```

   A context names one cluster and at most one user; a context with no user gives no client certificate, for a plain HTTP loopback server. A name is unique within its list and not empty. An empty file, or one with no `current-context`, selects nothing.
2. **A cluster's `server` is the compute root, as `--server` is.** It is the same string `--server` takes, so the derivation of the other groups' roots in ADR 0015 §1 applies to it unchanged, and a `--server` or `GAON_SERVER` replaces it with a value of the same meaning. The alternative, a base address from which the CLI appends the compute suffix, would give the file and the flag two meanings for one word, and a user who copies a `--server` value into a file would get a doubled path.
3. **Credentials are paths.** `certificate-authority`, `client-certificate` and `client-key` name files; the format has no field that embeds their content. A relative path is taken relative to the directory of the config file, not of the working directory. A user names both a certificate and a key or neither. The CA belongs to the cluster, because it is what that server's certificate chains to, and the certificate and key to the user, because they are who is calling.
4. **Where the file is.** `--config FILE`, else `GAON_CONFIG`, else `.gaon/config` under the home directory (`os.UserHomeDir`). A file named by the flag or the variable must exist. The default may be absent, and then there is no file and nothing changes from before this ADR. `--config` comes before the command, like `--server`.
5. **Ranking.** A flag beats the environment, which beats the file:
   - the server: `--server`, then `GAON_SERVER`, then the current context's cluster;
   - each of the three TLS files separately: `--tls-ca`, `--tls-cert` or `--tls-key`, then the file's value. They have no environment variable, as before. The merged three must still be all or none, so a flag can replace one file of the identity;
   - the file itself: `--config`, then `GAON_CONFIG`, then the default.
6. **The current context is always resolved**, so a `--server` does not hide a file that is broken. Resolving checks that the current context exists, that it names a cluster that is defined and has a `server`, and that a user it names is defined and complete.
7. **Refusals are usage errors** (exit code 2) that name the file: a file that is missing and was named, that is not YAML or has a field the format lacks, that repeats or omits a name, a current context that names no context, a context that names no cluster or a cluster or user that is not defined, a cluster with no server, a user with only one of its two files. No request is sent. With no server from any source the message adds the config file's current context to the sources it names.
8. **`gaon config current-context`** prints the name of the current context. **`gaon config use-context NAME`** sets it and writes the file back. Both need the file to exist whether or not something named it, and neither needs a server. A `use-context` of a name that is not a context is a usage error and leaves the file as it was. A file with no current context makes `current-context` exit 1.
9. **Credentials are never printed or logged.** The CLI prints the names of contexts and the paths in an error, and reads the files only to build the client.

## Consequences

- **A call against a mutual TLS server is `gaon get machine` once the file holds the cluster and the user**, and `--server` or `GAON_SERVER` still point one call elsewhere.
- **A `--server` aimed at another server still presents the context's certificate**, and trusts only the context's CA. Reaching a server that chains to another CA takes `--tls-ca` as well.
- **A cluster with a CA and a context with no user is refused** by the all-or-none rule of `lib/api/`, with a message that names the flags it lacks, because that is the rule's wording.
- **`use-context` rewrites the file from its parsed form**, so comments and layout are lost, and entries come back in the format's own order. A hand-edited file that must keep its comments is edited by hand.
- **A relative path depends on where the file is, not on where the command runs**, so one file works from any directory.
- **The format is strict**, so a file written for a later version that adds a field is refused by this one, naming the file.
- **There is no `--context` flag.** Which cluster a call reaches is the current context, `--server`, or `GAON_SERVER`.
- **Rejected: embedded certificate data.** It would put key material in a file that is otherwise paths, make `use-context` rewrite it, and add a second way to supply each file, for no case the issue names.
- **Rejected: a cluster entry holding the API server's base address.** Decision 2 says why.
- **Rejected: an environment variable for each TLS file.** The file replaces the need the issue names, and a variable per file is three more names to rank.
- **Rejected: a missing default file being an error.** A user with no file would meet an error on every command that worked before.
- **Rejected: resolving the current context only when no `--server` is given.** A broken file would then work or fail by which flags a call carried.
- **Rejected: editing the file in place to keep its comments.** It needs the YAML node tree and its own tests for one command; the cost is the lost comments noted above, taken until someone asks.

## Signs the decision was wrong

- Users keep comments in the file and `use-context` is reported to destroy them.
- Users copy one file between machines and relative paths or the home directory default are the common cause of failures.
- A deployment needs the key material in the file, or a bearer token, and the format is asked for a field.
- `--server` is used so often with a context's credentials that a `--context` flag is asked for.
- The file grows a field the strict decoder refuses in one version and another accepts, and users run both.
