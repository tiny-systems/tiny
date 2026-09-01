# Contributing

Issues, questions and PRs are welcome. The codebase is small on purpose;
reading it end to end is a reasonable afternoon.

## Dev loop

```
go build ./...
make test        # runs with -race; bare `go test` hides races
make lint
```

For running against a real cluster, minikube or kind works:

```
kind create cluster
go run ./cmd/tiny init --context kind-kind -n tiny --yes
go run ./cmd/tiny new "hello"
```

Local images: build `images/agent` and the controller with your cluster's
docker daemon and point the `tiny-settings` ConfigMap keys `agentImage` /
`sidecarImage` at your tags — that override is the dev loop's home.

## Ground rules

- `make test` and `make lint` green before a PR; CI runs both.
- Fail loud: a wedged pod with no message is worse than a failure. New
  error paths should say what broke in the fleet row or the CLI output.
- The security invariants are not up for casual trade: agents hold no
  credentials, the sidecar stays powerless, exec stays argv-only. PRs
  that weaken one need a very good story.
