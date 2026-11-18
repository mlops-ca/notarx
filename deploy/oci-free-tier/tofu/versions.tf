terraform {
  required_version = ">= 1.8.0"

  required_providers {
    oci = {
      source  = "oracle/oci"
      version = "~> 8.0"
    }
  }
}

provider "oci" {
  # Authentication comes from the environment or ~/.oci/config (see README.md);
  # no credential is set here.
  region = var.region
}
