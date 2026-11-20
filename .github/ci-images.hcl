# The images the CI test suite loads (docs/ci.md, Shared images). The
# build-images-<arch> jobs of test-suite.yml build both targets in parallel in one
# job per architecture, natively on that architecture's runner (ADR 0016), through
# the .github/actions/ci-images composite action. The variables come from the
# environment of that step.
variable "RUNNER_ARCH" {
  # X64 or ARM64; keeps the BuildKit cache of each architecture apart.
}

variable "GOMOD_SEED" {
  # The verified Go module download directory (the gomod-seed build context).
}

variable "OUTPUT_DIR" {
  # Where the image tarballs are written for upload.
}

group "default" {
  targets = ["notarx", "apt-signer"]
}

target "seeded" {
  context  = "."
  contexts = { gomod-seed = GOMOD_SEED }
}

target "notarx" {
  inherits   = ["seeded"]
  dockerfile = "Dockerfile"
  tags       = ["notarx:local"]
  cache-from = ["type=gha,scope=ci-notarx-${RUNNER_ARCH}"]
  cache-to   = ["type=gha,mode=max,scope=ci-notarx-${RUNNER_ARCH}"]
  output     = ["type=docker,dest=${OUTPUT_DIR}/notarx.tar"]
}

target "apt-signer" {
  inherits   = ["seeded"]
  dockerfile = "Dockerfile.apt-signer"
  tags       = ["notarx-reference-apt-signer:local"]
  cache-from = ["type=gha,scope=ci-apt-signer-${RUNNER_ARCH}"]
  cache-to   = ["type=gha,mode=max,scope=ci-apt-signer-${RUNNER_ARCH}"]
  output     = ["type=docker,dest=${OUTPUT_DIR}/apt-signer.tar"]
}
