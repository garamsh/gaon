# lib/api

The Go types, validation, watch event shape and HTTP client of the gaon `compute.gaon.garam.sh/v1alpha1` resource API.

A unit imports it by module path, `github.com/garamsh/gaon/lib/api`, and resolves it through the repository's `go.work`:

```go
import "github.com/garamsh/gaon/lib/api"
```

The contract these types implement is in [docs/architecture/resource-api.md](../../docs/architecture/resource-api.md).

## Building a client

A unit that calls the API takes `--tls-ca`, `--tls-cert` and `--tls-key`, and builds its client from them:

```go
files := api.RegisterTLSFlags(flag.CommandLine)
flag.Parse()
httpClient, err := files.Client()
if err != nil {
	return err
}
c := api.NewClient(root, httpClient)
```

- All three flags are given, or none is. One or two given is an error naming the flags left out.
- With all three, the client speaks mutual TLS 1.3: it trusts only the certificates in `--tls-ca` and presents `--tls-cert` with `--tls-key`.
- With none, the client is plain HTTP, for a loopback server.
- A file that is missing or unreadable, a CA file with no certificate, and a key that does not match the certificate each fail with an error naming the file.
- `api.NewTLSClient(caFile, certFile, keyFile)` builds the same client from three paths, for a unit that reads no flags.
- The client sets no `Timeout`, so a watch may stay open.

## Calling with a bootstrap token

A caller that holds a bootstrap token and no certificate builds its client from the CA file alone and adds the token as an option:

```go
httpClient, err := api.NewServerTLSClient(caFile)
if err != nil {
	return err
}
c := api.NewClient(root, httpClient, api.WithBearerToken(token))
```

- `NewServerTLSClient` speaks TLS 1.3, trusts only the certificates in `caFile` and presents no certificate.
- `WithBearerToken` sends `Authorization: Bearer <token>` on every request, and the client refuses to send one over a URL that is not `https`.
- The option also works on a client from `NewTLSClient`, which keeps presenting its certificate.
- The API server lets a token create a CertificateSigningRequest and read one it created, and nothing else ([resource-api.md](../../docs/architecture/resource-api.md) §Transport).
