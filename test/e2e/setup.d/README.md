# E2E Cluster Setup

`run-e2e-gcp.sh` runs every `*.sh` script in this directory, in lexical order,
on the E2E VM after the kind cluster is up and before `go test` runs. Each
conformance test that needs cluster components (drivers, schedulers, operators)
owns one script here, so PRs adding different tests do not edit the same lines
of `run-e2e-gcp.sh`.

## Conventions

- Name scripts `NN-<component>.sh`. Scripts run in lexical order, and numbers
  need not be unique. Use `50-` by default. Use a lower number if your script
  must run before others (e.g. `10-cert-manager.sh`, whose webhooks others
  depend on), or a higher one if it must run after them.
- Scripts run with `bash` from the repository root with `kubectl`, `helm`, and
  `go` on `PATH`. Start with `set -o errexit -o nounset -o pipefail`.
- Read settings from environment variables with a default, e.g.
  `KUEUE_VERSION="${KUEUE_VERSION:-v0.18.2}"`. `run-e2e-gcp.sh` forwards any
  variable that a script reads with the `${NAME:-default}` form if it is set
  in the caller's environment, so no change to `run-e2e-gcp.sh` is needed to
  add an override.
- To pass flags to `go test`, append them to `"${E2E_TEST_ARGS_FILE}"`, one
  flag per line, e.g.
  `echo "-gang-scheduler-name=kueue" >>"${E2E_TEST_ARGS_FILE}"`.
