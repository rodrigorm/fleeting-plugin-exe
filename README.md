# fleeting-plugin-exe

A [GitLab Fleeting](https://gitlab.com/gitlab-org/fleeting/fleeting) provider plugin for autoscaling GitLab Runner jobs on exe.dev VMs.

> **Status:** early development. The provider lifecycle is not implemented yet.

## Intended lifecycle

| Fleeting operation | exe.dev operation |
|---|---|
| `Update` | list and reconcile owned VMs |
| `Increase` | create or clone VMs |
| `ConnectInfo` | return SSH routing information |
| `Heartbeat` | verify VM/SSH health |
| `Decrease` | delete VMs |

The plugin will only manage VMs carrying its configured ownership and fleet tags.

## Development

Requires Go 1.26 or newer.

```shell
make test
make build
```

## License

MIT
