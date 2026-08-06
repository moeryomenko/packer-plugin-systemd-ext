The systemd-ext plugin adds two provisioners to Packer that apply systemd
extension images during an image build:

- [systemd-ext-sysext](/packer/plugins/provisioners/systemd-ext-sysext) merges
  extensions over `/usr`.
- [systemd-ext-confext](/packer/plugins/provisioners/systemd-ext-confext)
  merges extensions over `/etc`.

Both provisioners run with one of two persistence strategies:

- **bake** — the merged content is copied into the base image rootfs and the
  extension images are removed.
- **persist** — the extension images stay in `/var/lib/extensions` (sysext) or
  `/var/lib/confexts` (confext) and systemd re-merges them at every boot.

### Installation

To install this plugin, copy and paste this code into your Packer
configuration, then run [`packer init`](https://developer.hashicorp.com/packer/docs/commands/init).

```hcl
packer {
  required_plugins {
    systemd-ext = {
      source  = "github.com/eryoma/systemd-ext"
      version = "~> 0.1"
    }
  }
}
```

Alternatively, build the plugin from source and install it with
`packer plugins install`:

```sh
$ make build
$ packer plugins install --path ./packer-plugin-systemd-ext systemd-ext
```

### Components

#### Provisioners:

- [systemd-ext-sysext](/packer/plugins/provisioners/systemd-ext-sysext) - Applies
  systemd-sysext extension images, merging them over /usr.
- [systemd-ext-confext](/packer/plugins/provisioners/systemd-ext-confext) - Applies
  systemd-confext extension images, merging them over /etc.
