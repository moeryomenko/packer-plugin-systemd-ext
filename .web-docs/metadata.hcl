integration {
  name = "systemd-ext"
  description = "The systemd-ext plugin provides sysext and confext provisioners that merge systemd extension images over /usr and /etc during a Packer build."
  identifier = "packer/eryoma/systemd-ext"
  component {
    type = "provisioner"
    name = "systemd-ext-sysext"
    slug = "systemd-ext-sysext"
  }
  component {
    type = "provisioner"
    name = "systemd-ext-confext"
    slug = "systemd-ext-confext"
  }
}
