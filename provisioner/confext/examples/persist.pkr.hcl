# systemd-ext-confext in persist mode.
#
# Persist mode leaves each configuration extension image in /var/lib/confexts
# and merges it on every boot via systemd-confext.service.
# merge_during_build also applies the extension during the build so you can
# assert the merged result before the artifact is finished.
#
# The provisioner label is namespaced by Packer as <plugin>-<component>: the
# binary is packer-plugin-systemd-ext, so the usable label is
# "systemd-ext-confext".

source "qemu" "ubuntu-24-04" {
  iso_url      = "https://cloud-images.ubuntu.com/releases/24.04/release/ubuntu-24.04-server-cloudimg-amd64.img"
  iso_checksum = "file:https://cloud-images.ubuntu.com/releases/24.04/release/SHA256SUMS"
  communicator = "ssh"
  ssh_username = "root"
  ssh_password = "packer"
}

build {
  sources = ["source.qemu.ubuntu-24-04"]

  provisioner "systemd-ext-confext" {
    mode               = "persist"
    merge_during_build = true

    extensions {
      name   = "corp-config"
      source = "${path.root}/extensions/corp-config" # contents become /etc/...
    }
    # Prebuilt .raw disk image: uploaded as-is; merge and verification are
    # delegated to systemd. The image must carry its own extension-release
    # file matching the guest os-release.
    # extensions {
    #   name   = "prebuilt-config"
    #   source = "${path.root}/prebuilt/prebuilt-config.raw"
    # }
  }

  # merge_during_build=true merges in this session, so the file is visible at
  # its merged /etc path when this shell runs.
  provisioner "shell" {
    inline = [
      "test -f /etc/corp-config/main.conf && echo 'confext merged OK'",
    ]
  }
}
