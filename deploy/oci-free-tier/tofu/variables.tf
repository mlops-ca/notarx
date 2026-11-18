variable "region" {
  type        = string
  description = "OCI region identifier of the tenancy's home region, for example ca-toronto-1."
}

variable "tenancy_ocid" {
  type        = string
  description = "OCID of the tenancy. IAM users and groups are created at this level."
}

variable "compartment_ocid" {
  type        = string
  description = "OCID of the compartment that holds every resource and that the backup policy is scoped to."
}

variable "name" {
  type        = string
  description = "Name prefix for the resources."
  default     = "notarx"
}

variable "ssh_public_key" {
  type        = string
  description = "SSH public key (one line) that may log in as ubuntu."
}

variable "ssh_allowed_cidr" {
  type        = string
  description = "The only CIDR allowed to reach port 22, for example 203.0.113.7/32. Never 0.0.0.0/0."

  validation {
    condition     = var.ssh_allowed_cidr != "0.0.0.0/0" && can(cidrhost(var.ssh_allowed_cidr, 0))
    error_message = "ssh_allowed_cidr must be a valid CIDR and must not be 0.0.0.0/0."
  }
}

variable "ocpus" {
  type        = number
  description = "OCPUs of the A1 instance (ADR 0017 reference: 2)."
  default     = 2
}

variable "memory_gbs" {
  type        = number
  description = "Memory in GB of the A1 instance (ADR 0017 reference: 12)."
  default     = 12
}

variable "boot_volume_gbs" {
  type        = number
  description = "Boot volume size in GB (the Always Free minimum is 50)."
  default     = 50

  validation {
    condition     = var.boot_volume_gbs >= 50
    error_message = "The boot volume must be at least 50 GB."
  }
}

variable "data_volume_gbs" {
  type        = number
  description = "Data volume size in GB. The Always Free block storage budget is 200 GB including the boot volume."
  default     = 150

  validation {
    condition     = var.data_volume_gbs >= 50
    error_message = "The data volume must be at least 50 GB."
  }
}

variable "availability_domain_index" {
  type        = number
  description = "Index of the availability domain to try; change it when the region reports out of host capacity."
  default     = 0
}

variable "vcn_cidr" {
  type        = string
  description = "CIDR of the VCN and its single public subnet."
  default     = "10.20.0.0/24"
}
