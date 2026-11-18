# Optional OpenTofu module for the OCI Always Free reference deployment
# (ADR 0017, slice 7). It creates exactly what the runbook's console steps create.
# See README.md for state handling: the state holds the customer secret key.

data "oci_identity_availability_domains" "this" {
  compartment_id = var.tenancy_ocid
}

data "oci_objectstorage_namespace" "this" {
  compartment_id = var.tenancy_ocid
}

data "oci_core_images" "ubuntu" {
  compartment_id           = var.compartment_ocid
  operating_system         = "Canonical Ubuntu"
  operating_system_version = "24.04"
  shape                    = "VM.Standard.A1.Flex"
  sort_by                  = "TIMECREATED"
  sort_order               = "DESC"
}

locals {
  availability_domain = data.oci_identity_availability_domains.this.availability_domains[var.availability_domain_index].name
}

# ---- Network ----------------------------------------------------------------

resource "oci_core_vcn" "this" {
  compartment_id = var.compartment_ocid
  display_name   = "${var.name}-vcn"
  cidr_blocks    = [var.vcn_cidr]
  dns_label      = "notarx"
}

resource "oci_core_internet_gateway" "this" {
  compartment_id = var.compartment_ocid
  vcn_id         = oci_core_vcn.this.id
  display_name   = "${var.name}-igw"
  enabled        = true
}

resource "oci_core_route_table" "public" {
  compartment_id = var.compartment_ocid
  vcn_id         = oci_core_vcn.this.id
  display_name   = "${var.name}-public"

  route_rules {
    destination       = "0.0.0.0/0"
    destination_type  = "CIDR_BLOCK"
    network_entity_id = oci_core_internet_gateway.this.id
  }
}

# 22 from one CIDR, 80 and 443 from anywhere; nothing else is open (ADR 0017).
resource "oci_core_security_list" "this" {
  compartment_id = var.compartment_ocid
  vcn_id         = oci_core_vcn.this.id
  display_name   = "${var.name}-public"

  egress_security_rules {
    destination = "0.0.0.0/0"
    protocol    = "all"
  }

  ingress_security_rules {
    source   = var.ssh_allowed_cidr
    protocol = "6"
    tcp_options {
      min = 22
      max = 22
    }
  }

  ingress_security_rules {
    source   = "0.0.0.0/0"
    protocol = "6"
    tcp_options {
      min = 80
      max = 80
    }
  }

  ingress_security_rules {
    source   = "0.0.0.0/0"
    protocol = "6"
    tcp_options {
      min = 443
      max = 443
    }
  }
}

resource "oci_core_subnet" "public" {
  compartment_id             = var.compartment_ocid
  vcn_id                     = oci_core_vcn.this.id
  display_name               = "${var.name}-public"
  cidr_block                 = var.vcn_cidr
  dns_label                  = "public"
  route_table_id             = oci_core_route_table.public.id
  security_list_ids          = [oci_core_security_list.this.id]
  prohibit_public_ip_on_vnic = false
}

# ---- Compute ----------------------------------------------------------------

resource "oci_core_instance" "this" {
  compartment_id      = var.compartment_ocid
  availability_domain = local.availability_domain
  display_name        = var.name
  shape               = "VM.Standard.A1.Flex"

  shape_config {
    ocpus         = var.ocpus
    memory_in_gbs = var.memory_gbs
  }

  source_details {
    source_type             = "image"
    source_id               = data.oci_core_images.ubuntu.images[0].id
    boot_volume_size_in_gbs = var.boot_volume_gbs
  }

  create_vnic_details {
    subnet_id        = oci_core_subnet.public.id
    assign_public_ip = true
  }

  metadata = {
    ssh_authorized_keys = var.ssh_public_key
    user_data           = base64encode(file("${path.module}/../cloud-init.yaml"))
  }
}

resource "oci_core_volume" "data" {
  compartment_id      = var.compartment_ocid
  availability_domain = local.availability_domain
  display_name        = "${var.name}-data"
  size_in_gbs         = var.data_volume_gbs
}

resource "oci_core_volume_attachment" "data" {
  instance_id = oci_core_instance.this.id
  volume_id   = oci_core_volume.data.id
  # Cloud-init expects /dev/oracleoci/oraclevdb.
  attachment_type = "paravirtualized"
  device          = "/dev/oracleoci/oraclevdb"
}

# ---- Backups ----------------------------------------------------------------

resource "oci_objectstorage_bucket" "backups" {
  compartment_id = var.compartment_ocid
  namespace      = data.oci_objectstorage_namespace.this.namespace
  name           = "${var.name}-backups"
  access_type    = "NoPublicAccess"
  versioning     = "Disabled"
}

resource "oci_identity_user" "backup" {
  compartment_id = var.tenancy_ocid
  name           = "${var.name}-backup"
  description    = "notarx backups to Object Storage (S3-compatible API)"
}

resource "oci_identity_group" "backup" {
  compartment_id = var.tenancy_ocid
  name           = "${var.name}-backup"
  description    = "notarx backup writers"
}

resource "oci_identity_user_group_membership" "backup" {
  user_id  = oci_identity_user.backup.id
  group_id = oci_identity_group.backup.id
}

# Least privilege: objects in one bucket of one compartment, nothing else.
resource "oci_identity_policy" "backup" {
  compartment_id = var.compartment_ocid
  name           = "${var.name}-backup"
  description    = "notarx backup user: manage objects in the backup bucket only"
  statements = [
    "Allow group ${oci_identity_group.backup.name} to manage objects in compartment id ${var.compartment_ocid} where target.bucket.name='${oci_objectstorage_bucket.backups.name}'",
  ]
}

# The secret is shown once and is stored in the state: use remote encrypted state.
resource "oci_identity_customer_secret_key" "backup" {
  user_id      = oci_identity_user.backup.id
  display_name = "${var.name}-backup"
}
