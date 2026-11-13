# lib/plugin

The gaon plugin API, `gaon.plugin.v1alpha1`: the gRPC services a Node Agent calls on a network plugin and a storage plugin, each a process of its own, and the Go server and client for them.

A plugin imports it by module path, `github.com/garamsh/gaon/lib/plugin`, and needs nothing else of gaon but `lib/api`. What each call means, the socket directory, discovery and what the agent does when a plugin is not running are in [docs/architecture/node-agent.md](../../docs/architecture/node-agent.md) §Plugins and [ADR 0019](../../docs/architecture/adr/0019-the-node-agent-reaches-its-network-and-storage-plugins-as-separate-processes-through-a-versioned-grpc-api-on-unix-sockets.md).

## Writing a plugin

```go
type policy struct{}

func (policy) Policy(ctx context.Context, m *api.MicroVM) (*plugin.NetworkPolicy, error) {
	return &plugin.NetworkPolicy{}, nil // or a *plugin.Refusal for a MicroVM it does not serve
}

err := plugin.ServeNetwork(ctx, plugin.DefaultDir, "mynet", policy{})
```

A network plugin that also enforces NetworkPolicy implements `plugin.Enforcer`, whose `Enforce(ctx, limits)` receives the complete set of limits of the MicroVMs on its Node on every call and replaces what it enforced; a plugin that does not implement it is answered `Unimplemented` and left to its create-time policy.

`ServeStorage` does the same for a `plugin.Storage`. The socket is `<dir>/network-mynet.sock`, and the agent is started with `--network-plugin mynet`.

## The proto

`proto/gaon/plugin/v1alpha1/plugin.proto` is the contract's encoding and `v1alpha1/` holds the generated Go, committed. To regenerate after a change, from this directory:

```
buf lint
buf generate
```

`buf.gen.yaml` runs `protoc-gen-go` and `protoc-gen-go-grpc` at the versions it names, through `go run`.
