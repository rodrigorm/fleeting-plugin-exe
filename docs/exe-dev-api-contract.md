# exe.dev Fleeting-provider client contract (official docs only)

**Research date:** 2026-09-04  
**Scope:** Published exe.dev documentation at `exe.dev/docs` only. No live CLI calls, undocumented endpoints, or inferred JSON schemas are used.

## Live validation supplement

The following behavior was verified against a live exe.dev account on 2026-09-04. It supplements, but does not replace, the published contract:

- `whoami --json` and `ls -l --json` succeed through a dedicated registered SSH key.
- `ls -l --json` returns the documented `vms` array plus additional fields.
- Untagged VMs omit the `tags` member entirely.
- Tagged VMs return `tags` as an array of strings.
- The live `tag` command reported the accepted tag grammar as `[a-z][a-z0-9_-]*`.
- `ssh_dest` and `ssh_host` were present; `ssh_user` was omitted where any username can route.
- `new --json` returned the requested VM name, tags, SSH routing fields, HTTPS URL, and proxy port.
- A VM created with 1 vCPU, 3 GB memory, and 25 GB disk was immediately listed as `running` with matching resource fields.
- Omitting `--image` selected the account's default `boldsoftware/exeuntu` image. Supplying `--image=boldsoftware/exeuntu` failed because that registry reference was not directly pullable; the plugin therefore treats image as optional.
- The worker accepted routed SSH with the same pinned exe.dev host key and exposed Docker 29.1.3 using `overlayfs`.
- `rm --json` returned separate `deleted` and `failed` arrays, and the VM disappeared from inventory immediately in this probe.

A temporary probe tag was added to the Queen VM, observed in `ls -l --json`, and removed immediately. A separate disposable worker completed a create, inventory, SSH/Docker, and delete cycle. No probe resources remain.

## 1. Transport and command execution

The documented programmatic API is SSH:

```sh
ssh exe.dev ls --json
ssh exe.dev new --json
```

The documented HTTPS equivalent is exactly one endpoint:

```http
POST https://exe.dev/exec
Authorization: Bearer <token>

<body> = the SSH command text, e.g.:
ssh my-vm whoami
```

For HTTPS API responses, JSON output is always enabled (equivalent to `--json`). The response body is the command's combined output; stderr is mixed into it. `/exec` has no stdin, no PTY, a 30-second timeout, and a 64 KiB request-body limit. The command syntax is parsed twice (lobby shell lexer, then VM/sshd-like parser), so quoting must survive both layers.

For direct VM access, the documented lobby command is:

```text
ssh [-l user] [user@]vmname [command...]
```

The docs also show direct routing as `ssh vmname.exe.xyz` and say `ssh_dest` is a ready-to-use `ssh`/`scp` destination. **Use `ssh_dest` from listing output rather than constructing a host.**

## 2. `ls --json`

Documented command syntax:

```text
ls [-l] [--group=tag|region|type] [name|pattern]
```

Options documented:

- `--json`: JSON output.
- `-l`: detailed information.
- `--group`: option text lists `none|tag|region|type`; the usage line lists `tag|region|type` (documentation inconsistency). Do not depend on `none` unless verified against the target CLI.

The only published JSON example is:

```json
{
  "vms": [
    {
      "https_url": "https://bloggy.exe.xyz",
      "region": "lon",
      "region_display": "London, UK",
      "ssh_dest": "bloggy.exe.xyz",
      "ssh_host": "bloggy.exe.xyz",
      "status": "running",
      "vm_name": "bloggy"
    }
  ]
}
```

Documented fields in that example:

- Top-level `vms` array (the docs address `.vms[0]`).
- Per-item `https_url`, `region`, `region_display`, `ssh_dest`, `ssh_host`, `status`, `vm_name`.
- `ssh_dest`: ready-to-use `ssh`/`scp` destination; may include a username prefix such as `vm+bloggy@vm.exe.xyz` when hostname routing is insufficient.
- `ssh_host`: network host to dial.
- `ssh_user`: SSH routing username; absent when any username works. It is described but **not present in the published sample**.

**Undocumented/unknown:** complete item schema; field types beyond the JSON example; allowed `status` values; empty-list/top-level behavior; fields returned by `-l`; whether tags, resources, comments, pool, owner, or timestamps are present; pagination; exact pattern matching semantics; and whether `--group` changes the JSON envelope. A client must not require undocumented fields.

## 3. `new --json`

The page exposes options but no usage line or JSON response example. Use:

```text
new [options] --json
```

Documented options and exact constraints/examples:

- `--comment`: short note, max **200 bytes**.
- `--cpu`: number of CPUs; default **2**; plan limits via `billing plan`.
- `--disk`: disk size; examples `20`, `20GB`, `50G`.
- `--env`: `KEY=VALUE`; repeatable.
- `--image`: container image.
- `--integration`: repeatable or comma-separated.
- `--memory`: examples `4`, `4GB`, `8G`.
- `--name`: VM name; auto-generated if omitted.
- `--no-email`: suppress email notification.
- `--pool`: create in one of the team's pools.
- `--prompt`: initial Shelley prompt; requires an image with Shelley, such as exeuntu; `/dev/stdin` reads stdin.
- `--registry-auth`: `USERNAME:PASSWORD`, example `octocat:ghp_xxx`, for the registry hosting `--image`.
- `--setup-script`: first-boot setup script; max **10 KiB**; supports literal `\\n` for newlines; `/dev/stdin` reads stdin.
- `--tag`: repeatable or comma-separated; example `--tag=prod,staging`.
- `--json`: JSON output.

Published examples include:

```sh
new
new --name=b --image=ubuntu:22.04
new --cpu=4 --memory=16GB
new --disk=50GB
new --tag=prod,staging
new --env FOO=bar --env BAZ=qux
new --integration=myproxy
echo 'build me a web app' | ssh exe.dev new --prompt=/dev/stdin
```

**Undocumented/unknown:** JSON envelope and all response fields; exact defaults for memory/disk/image/tags; accepted numeric ranges and unit grammar; name charset/length/normalization; tag charset/length; ordering/escaping of repeated flags; and creation readiness guarantees. The docs only say `--cpu` defaults to 2 and `--name` can be auto-generated.

## 4. `cp` (relevant if cloning)

```text
cp <source-vm> [new-name] [--pool=<name>] [--memory=<size>] [--cpu=<count>] [--disk=<size>] --json
```

Options:

- `--copy-tags`: copy source tags; explicit `--copy-tags=false` disables it.
- `--cpu`, `--memory`, `--disk`: resource overrides; examples use `--cpu=4 --memory=16GB`.
- `--pool`: destination team pool.
- `--json`: JSON output.

Examples:

```sh
cp my-vm
cp my-vm my-vm-copy
cp my-vm --cpu=4 --memory=16GB
cp my-vm my-pool-copy --pool=build
cp my-vm --copy-tags=false
```

**Undocumented/unknown:** JSON response shape/fields, whether tag-copy is formally default-true versus merely implied by the option description, resource defaults/validation, readiness semantics, and naming rules.

## 5. `rm --json`

```text
rm <vmname>... --json
```

Multiple VM names are accepted; `--json` is documented.

**Undocumented/unknown:** success JSON shape, per-name result format, partial-failure behavior, idempotency/not-found behavior, and whether deletion is synchronous.

## 6. `tag`

```text
tag [-d] <vm> <tag-name> [tag-name...] --json
```

- Without `-d`: add the listed tags.
- With `-d`: delete the listed tags.
- The examples use space-separated positional tag names:

```sh
tag my-vm prod web
tag -d my-vm prod web
```

At creation, tags use a different documented grammar: `--tag` may be repeated or comma-separated (`new --tag=prod,staging`).

**Undocumented/unknown:** tag JSON response shape, duplicate-tag behavior, missing-tag deletion behavior, empty tag handling, tag charset/length/normalization, and whether comma-separated values are accepted by the standalone `tag` command. Do not pass comma-joined values to `tag` based on the docs; use separate positional arguments.

## 7. `stat`

```text
stat <vm-name> [--range=24h|7d|30d] --json
```

Purpose: vCPU, disk, I/O, and network RX/TX metrics. `--range` accepts `24h`, `7d`, or `30d`; default is documented as the last 24 hours.

**Undocumented/unknown:** JSON envelope, metric field names/types/units, timestamp format, sample granularity, missing-data behavior, and whether a stopped VM has metrics.

## 8. Resource flags and limits

Creation (`new`) and cloning (`cp`) expose `--cpu`, `--memory`, and `--disk`. Documented examples are CPU integer values (`4`), memory values (`4`, `4GB`, `8G`), and disk values (`20`, `20GB`, `50G`). `new --cpu` defaults to 2; other defaults are not stated. `billing plan --json` is documented as the way to show current plan/resource limits, but its JSON schema is not published.

Resize, if needed later:

```text
resize <vmname> [--memory=<size>] [--cpu=<count>] [--disk=<size>] --json
```

`resize --disk` is a new total and **must be larger than the current size**.

The docs describe vCPU/RAM as a shared account/team pool, not a per-VM entitlement. Do not hard-code plan limits from docs; query `billing plan` only if the client has a documented need and can tolerate its undocumented response schema.

## 9. Setup-script behavior

The exeuntu image runs `/exe.dev/setup` at first boot, once. `new --setup-script` can supply it. Official CLI docs cap the supplied script at **10 KiB**, support `\\n` for newlines, and support `/dev/stdin` for piping. The docs recommend indirection for scripts near/over the maximum.

**Undocumented/unknown:** whether 10 KiB means bytes after escape decoding, exact rejection status/message, shell/interpreter rules for arbitrary images, whether script failure affects VM status, and whether the script runs before the VM is reported by `ls`.

## 10. Naming restrictions

The official docs state that `--name` is a VM name, auto-generated when omitted, and use names such as `my-vm`, `bloggy`, and `lynx-zebra`. They do **not** publish an authoritative VM-name grammar or limits.

Therefore the client must not invent a regex or length restriction. **Unknown:** allowed characters, case sensitivity, maximum/minimum length, Unicode, reserved names, normalization, uniqueness/conflict semantics, and whether names must be valid DNS labels. Treat server validation as authoritative; use a conservative configured prefix only if the caller supplies one.

## 11. SSH routing and ConnectInfo implications

For each listed VM, prefer:

1. `ssh_dest` as the complete destination for `ssh`, `scp`, and similar tools.
2. If splitting destination parts is unavoidable, use documented `ssh_host` and optional `ssh_user`; do not parse routing rules or synthesize `vmname.exe.xyz` when `ssh_dest` differs.
3. For command execution through the lobby, send `ssh <vm-name> <command...>` to `ssh exe.dev` (or as the `/exec` POST body).

The docs do not specify Fleeting-specific connect fields, SSH port, host-key policy, connect timeout, retry/backoff, or readiness transition semantics.

## 12. Errors and exit status

For HTTPS `/exec`, documented HTTP statuses are:

- `401`: invalid token (malformed, expired, unknown key, or bad signature).
- `400`: empty/missing body or invalid command syntax.
- `403`: command not allowed by token permissions.
- `404`: unknown command.
- `405`: method other than POST.
- `413`: request body over 64 KiB.
- `422`: command ran but returned non-zero; body contains error message.
- `504`: command exceeded 30 seconds.
- `429`: rate limited per SSH key.
- `500`: unexpected server-side error.

For command execution, the exit code is documented in the `X-Exe-Exit` trailer. Example:

```text
X-Exe-Exit: 1
```

The docs do not define a JSON error envelope, stable error codes/messages for CLI commands, or regular SSH client's process-exit mapping. Preserve raw stdout/stderr and inspect the SSH process status; for HTTPS also inspect HTTP status and `X-Exe-Exit`.

## 13. Token permissions (only if using HTTPS)

Generate an API token, for example:

```sh
ssh exe.dev ssh-key generate-api-key --label=ci --cmds=ls,new --exp=90d
```

`cmds` entries are command names; flags such as `--json` are allowed when the base command is allowed. Subcommands must be explicit (`ssh-key list`, not merely `ssh-key`). For VM execution:

```sh
ssh exe.dev ssh-key generate-api-key --label=deploy --cmds=ssh --exp=30d
ssh exe.dev ssh-key generate-api-key --label=deploy --cmds="'ssh my-vm'" --exp=30d
```

**Unknown:** token-generation JSON response schema and whether all lifecycle commands are available under a given account/token policy.

## Source URLs (official exe.dev docs)

- https://exe.dev/docs/api
- https://exe.dev/docs/cli-ls
- https://exe.dev/docs/cli-new
- https://exe.dev/docs/cli-cp
- https://exe.dev/docs/cli-rm
- https://exe.dev/docs/cli-tag
- https://exe.dev/docs/cli-stat
- https://exe.dev/docs/cli-resize
- https://exe.dev/docs/cli-ssh
- https://exe.dev/docs/https-api
- https://exe.dev/docs/https-api-run-on-vm
- https://exe.dev/docs/cli-billing
- https://exe.dev/docs/faq/how-exedev-works
- https://exe.dev/docs/faq/tab-completion
- https://exe.dev/docs/all
