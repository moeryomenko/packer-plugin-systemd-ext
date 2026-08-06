Type: `systemd-ext-sysext`

The `systemd-ext-sysext` provisioner applies systemd extension images
(sysext) to the image under construction. Each extension merges over `/usr`.
In **bake** mode the merged content is copied into the base rootfs and the
extension images are removed; in **persist** mode the images stay in
`/var/lib/extensions` and systemd-sysext re-merges them at every boot.

The label is namespaced by Packer as `<plugin>-<component>`: the binary is
`packer-plugin-systemd-ext`, so the usable provisioner label is
`systemd-ext-sysext`.

## Basic Example

```hcl
source "qemu" "ubuntu-24-04" {
  iso_url      = "https://cloud-images.ubuntu.com/releases/24.04/release/ubuntu-24.04-server-cloudimg-amd64.img"
  iso_checksum = "file:https://cloud-images.ubuntu.com/releases/24.04/release/SHA256SUMS"
  communicator = "ssh"
  ssh_username = "root"
  ssh_password = "packer"
}

build {
  sources = ["source.qemu.ubuntu-24-04"]

  provisioner "systemd-ext-sysext" {
    mode = "bake"

    extensions {
      name   = "corp-agent"
      source = "${path.root}/extensions/corp-agent"
    }
  }
}
```

Complete bake and persist templates ship in
`provisioner/sysext/examples/bake.pkr.hcl` and
`provisioner/sysext/examples/persist.pkr.hcl`.

## Configuration Reference

<!-- Code generated from the comments of the Config struct in provisioner/sysext/provisioner.go; DO NOT EDIT MANUALLY -->

- `extensions` ([]Extension) - Extensions lists the extension entries to apply.

- `mode` (string) - Mode is the persistence strategy: "bake" (default) or "persist".

- `merge_during_build` (bool) - MergeDuringBuild runs the merge during the build. Defaults to true when
  the key is omitted; false is a meaningful explicit value.

- `command` (string) - Command is the guest-side binary to invoke. Defaults to
  systemd-sysext.

- `guest_install_dir` (string) - GuestInstallDir is where extension images land on the guest. Defaults
  to /var/lib/extensions.

<!-- End of code generated from the comments of the Config struct in provisioner/sysext/provisioner.go; -->
