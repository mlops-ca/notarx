# OpenTofu module for the OCI Always Free reference deployment

Optional. It creates the OCI resources of the runbook
([docs/deployment/oci-free-tier.md](../../../docs/deployment/oci-free-tier.md)) so you
do not click through the console. The runbook lists the same steps by hand.

What it creates, in one compartment: a VCN with one public subnet and internet
notarx; a security list that opens 22 from one CIDR, and 80 and 443; one
VM.Standard.A1.Flex instance (2 OCPU, 12 GB by default, Ubuntu 24.04 arm64) that runs
`../cloud-init.yaml`; a block volume and its attachment; a private
Object Storage bucket for backups; an IAM user and group with a policy that lets them
manage objects in that one bucket of that one compartment; and a customer secret key for
the user.

## Use

```sh
cd deploy/oci-free-tier/tofu
cp terraform.tfvars.example terraform.tfvars   # edit; never commit
tofu init
tofu plan
tofu apply
tofu output public_ip s3_endpoint bucket_name s3_access_key
tofu output -raw s3_secret_key                  # the backup key; shown on demand only
```

Authenticate the provider with an OCI config profile or the `OCI_*` environment
variables; no credential is set in the module.

## State holds a secret

The customer secret key is stored in the state, so the state is as sensitive as the
key. Before the first `apply`:

- use a remote backend with encryption at rest and restricted access
  (`backend.tf.example` shows an OCI Object Storage bucket through its S3-compatible
  API), or any encrypted backend you already operate;
- add OpenTofu client-side state encryption through the `TF_ENCRYPTION` environment
  variable (see `backend.tf.example`), so the state is unreadable without the passphrase;
- never commit state, `terraform.tfvars` or `backend.tf` (the directory's
  `.gitignore` excludes them).

The `s3_secret_key` output is marked sensitive, so it is not printed by `plan` or `apply`.
The example files hold placeholders only.

## Checks

CI runs `tofu init -backend=false`, `tofu validate`, `tofu fmt -check` and `tflint` on
this directory (`make oci-free-tier-tofu-check`).
