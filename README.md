# packer-plugin-systemd-ext

A [Packer](https://www.packer.io) plugin that bakes or persists
[systemd extension images](https://www.freedesktop.org/software/systemd/man/latest/systemd-sysext.html)
into an image during a build:

- `systemd-ext-sysext` merges extension payloads over `/usr`.
- `systemd-ext-confext` merges configuration payloads over `/etc`.

Packer namespaces every literal component of a plugin as
`<plugin>-<component>`: because the binary is `packer-plugin-systemd-ext`, the
provisioners are declared in HCL as `provisioner "systemd-ext-sysext"` and
`provisioner "systemd-ext-confext"`.

## Installation

Build the plugin binary and install it from the local path:

```sh
make build
packer plugins install --path ./packer-plugin-systemd-ext systemd-ext
```

For a normal setup you can also add the plugin to your template and run
`packer init`:

```hcl
packer {
  required_plugins {
    systemd-ext = {
      source  = "github.com/moeryomenko/systemd-ext"
      version = "~> 0.1"
    }
  }
}
```

## Quick start

Minimal bake-mode sysext template (full examples ship in
`provisioner/sysext/examples/` and `provisioner/confext/examples/`):

```hcl
source "cloud-hypervisor" "ubuntu-24-04" {
  vcpus          = 2
  memory         = 2048
  firmware       = "/path/to/CLOUDHV.fd"
  disk_images {
    path       = "/path/to/ubuntu-24.04.raw"
    image_type = "raw"
  }
  network_interfaces {
    tap = "ch-tap-0"
    ip  = "10.0.2.2"
    # The cloud-hypervisor API requires a mask whenever an ip is set.
    mask = "255.255.255.0"
  }
  communicator = "ssh"
  ssh_username = "ubuntu"
  ssh_password = "ubuntu"
}

build {
  sources = ["source.cloud-hypervisor.ubuntu-24-04"]

  provisioner "systemd-ext-sysext" {
    mode = "bake"

    extensions {
      name   = "corp-agent"
      source = "${path.root}/extensions/corp-agent"
    }
  }
}
```

Each `extensions` entry takes a **directory source** (packaged by the plugin)
or a **prebuilt `.raw` disk image** (uploaded as-is; it must carry its own
`extension-release` file matching the guest). The block form
(`extensions { ... }`) is the validated syntax; the attribute-list form
(`extensions = [{ ... }]`) is not accepted by the generated HCL2 spec.

## Bake vs persist

| | bake (default) | persist |
|---|---|---|
| During build | extension merged, content copied into the rootfs | extension uploaded to `/var/lib/extensions` (sysext) or `/var/lib/confexts` (confext) |
| Extension images after build | removed | left in place |
| At boot | already part of the image | re-merged by `systemd-sysext.service` / `systemd-confext.service` |
| Use when | the extension must be part of the artifact itself | the image must keep applying the extension on the running system |

Persist runs keep the images so they can be updated in place; `mode =
"persist"` supports `merge_during_build` (defaults to `true`) to also apply
the extension during the build so its merged result can be asserted.

## Requirements

- **Guest systemd**: `systemd >= 252` for sysext, `systemd >= 254` for confext;
  the preflight fails fast with `SYSTEMD_TOO_OLD` otherwise.
- **Root SSH on the guest**: the preflight requires uid 0.
- **Host packaging tools** (only when a directory source uses `format =
  "squashfs"` / `"erofs"`): `mksquashfs` (squashfs-tools) or `mkfs.erofs`
  (`erofs-utils`). The default `format = "directory"` needs no extra tooling.
- **Go >= 1.25 and make** to build the plugin.

## Development

Requires **Go >= 1.25 and `make`** (Go 1.24+ is needed for the `tool`
directive; `.go-version` pins 1.25.11). All targets run from the repo root:

```sh
make build     # compile packer-plugin-systemd-ext
make generate  # regenerate hcl2spec + docs-partials + .web-docs (idempotent)
make generate-check  # fail if a second generate changes tracked files
make lint      # golangci-lint
make test      # gotestsum + race + coverage (writes coverage.out)
make fmt       # gofmt -s, golines, goimports
make vet       # go vet ./...
make tidy      # go mod tidy -v
make check     # lint + vet + test (CI gate)
make cover     # open the coverage report in a browser
make dev       # build with VersionPrerelease=dev and install into packer
```

External tools (`golangci-lint`, `gotestsum`, `golines`, `goimports`,
`gofumpt`) are pinned as Go tool dependencies in `tools/` (go.work
workspace) and run via `go tool`; `packer-sdc` runs via
`go run github.com/hashicorp/packer-plugin-sdk/cmd/packer-sdc`.

## End-to-end acceptance (E2E)

E2E boots a real VM under the `packer-plugin-cloud-hypervisor` builder
(pinned to commit `979d702db885417820fa27fbcfce42256c90e110`,
`CH_PLUGIN_REF`) and is **local-only**: there is no CI runner, because
GitHub-hosted runners have no `/dev/kvm`. `.github/workflows/e2e.yml` is a
`workflow_dispatch`-only scaffold for a self-hosted `kvm`-labeled runner; the
canonical gate is `make e2e` on a KVM-capable host.

Host prerequisites (checked fail-fast by the harnesses):

- `/dev/kvm` exists and is accessible
- `cloud-hypervisor` binary >= v38.0 on `$PATH` (or `ch_binary_path`)
- `packer` >= 1.9
- `qemu-img` — converts the pinned Ubuntu 24.04 qcow2 cloud image to raw
- Go >= 1.26 and `make` — builds this plugin and the pinned cloud-hypervisor
  plugin from source
- root or sudo — creates the `ch-tap-0` TAP device (`ip tuntap add`)

Run the gate:

```sh
make e2e          # bake harness then persist harness incl. chained
                  # second boot; stops on the first failure
make e2e-bake     # bake-mode harness only
make e2e-persist  # persist-mode harness only
SKIP_E2E=1 make e2e-bake  # explicit, loud skip — never a silent green
```

The harnesses live in `test/e2e/bake/` and `test/e2e/persist/`; each
directory's README documents the pinned assets, expected run time, and skip
policy.
