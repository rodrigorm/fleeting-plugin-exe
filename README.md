# fleeting-plugin-exe

A [GitLab Fleeting](https://gitlab.com/gitlab-org/fleeting/fleeting) provider plugin for autoscaling GitLab Runner jobs on exe.dev VMs.

> **Status:** experimental. The SSH API client and core Fleeting lifecycle are implemented and unit-tested. Live read-only access and tag serialization were validated against exe.dev on September 4, 2026; VM creation and deletion remain untested.

## Implemented lifecycle

| Fleeting operation | exe.dev operation |
|---|---|
| `Update` | `ssh exe.dev ls -l --json` and reconcile owned VMs |
| `Increase` | `ssh exe.dev new --json ...` |
| `ConnectInfo` | return documented `ssh_host` and optional `ssh_user` |
| `Heartbeat` | verify the VM remains listed, owned, and running |
| `Decrease` | `ssh exe.dev rm --json <name>` |

Suspend/resume is intentionally unsupported. The plugin only manages VMs carrying both its global ownership tag and exact fleet tag.

## Plugin configuration

```toml
[runners.autoscaler.plugin_config]
  fleet_id = "queen-a"
  name_prefix = "glr-queen-a-"
  max_size = 2
  control_host = "exe.dev"
  control_identity_file = "/etc/gitlab-runner/credentials/exe-control"
  control_known_hosts_file = "/etc/gitlab-runner/ssh/exe-known-hosts"
  image = "ubuntu:24.04"
  cpu = 1
  memory = "3GB"
  disk = "25GB"
  pool = ""
  extra_tags = ["team-ci"]
```

The manager's SSH configuration supplies a dedicated control-plane identity and a pinned exe.dev host key. The plugin bypasses ambient SSH configuration when both files are configured and forces batch mode, strict host-key checking, keepalives, and bounded command execution.

## Live-validation status

Validated against a live exe.dev account on September 4, 2026:

- dedicated-key authentication and pinned host-key verification;
- `whoami --json`;
- `ls -l --json`;
- tags are omitted for untagged VMs and returned as a string array for tagged VMs;
- tag names are accepted under the server-reported grammar `[a-z][a-z0-9_-]*`.

The provider therefore treats missing tags as normal for unrelated VMs, but fails closed when a fleet-prefixed VM lacks the required ownership tags. In-flight creation accounting prevents repeated scaling calls from exceeding `max_size` while a new VM is not yet visible.

Still unverified: `new --json` and `rm --json` behavior, creation readiness, and Docker connectivity to a newly created worker.

See [`docs/exe-dev-api-contract.md`](docs/exe-dev-api-contract.md) for the documented API surface and explicitly unknown response fields.

## Development

Requires Go 1.26 or newer.

```shell
make test
make build
```

## License

MIT
