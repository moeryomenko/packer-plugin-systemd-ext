# systemd-ext-confext in bake mode.
#
# Bake mode merges each configuration extension over /etc during the build,
# copies the merged content into the base image rootfs, and removes the
# extension images from /var/lib/confexts. Use bake when the
# configuration must be part of the image itself.
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
    mode = "bake"

    extensions {
      name   = "corp-config"
      source = "${path.root}/extensions/corp-config" # contents become /etc/...
    }
    # Prebuilt .raw disk image: uploaded as-is and baked via
    # systemd-dissect. The image must carry its own extension-release file
    # matching the guest os-release.
    # extensions {
    #   name   = "prebuilt-config"
    #   source = "${path.root}/prebuilt/prebuilt-config.raw"
    # }
  }

  # Bake mode copies the merged content into the rootfs, so the file is
  # already present at its /etc path when this shell runs.
  provisioner "shell" {
    inline = [
      "test -f /etc/corp-config/main.conf && echo 'confext baked OK'",
    ]
  }
}
