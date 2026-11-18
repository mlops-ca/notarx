# TFLint with its bundled Terraform ruleset (the OCI provider has no ruleset).
plugin "terraform" {
  enabled = true
  preset  = "recommended"
}
