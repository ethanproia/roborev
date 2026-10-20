# r3e5 proof at fb536f30

PAD 2026-10-20T19:00:00Z

Read-only for `cursor-cloud-stack-5b-wiring-r3e5`. This branch is
`cursor-cloud-stack-5b-r3e5-proof`, based on `fb536f30`. No force-push.
Tests used `ROBOREV_TELEMETRY_ENABLED=0`.

Green runs were on the unchanged `fb536f30` tree. Red runs were temporary
worktree edits and were not committed.

## cursorCloudCIRef

Definition: `internal/daemon/ci_poller.go:1885`.

Callers on `fb536f30`:

- `internal/daemon/ci_poller.go:1787`, inside `buildPanelOpts`, only when `agent.CanonicalName(m.Agent) == agent.CursorCloudHost` (`ci_poller.go:1786`).
- `internal/daemon/ci_poller_test.go:3310` and `:3314`, `TestCursorCloudCIRefUsesHeadOfRange`.

Verdict: reachable, and it does not launch cursor-cloud without explicit opt-in. A CI member is set to `cursor-cloud` only inside `if ciCursorCloudOptIn(repoCfg)` at `internal/daemon/ci_poller.go:1248`. That opt-in is `repoCfg.CI.Host` (`ci_poller.go:1157`). `applyCICursorCloudGate` drops a cursor-cloud member unless the pull request is same-repo and `[ci] host` is `cursor-cloud` (`ci_poller.go:1177`). The ref rewrite runs only after that member already exists.

Pin: `TestCISameRepoCIOptInLaunchesCursorCloud`.

Green, unchanged tree:

```
ok  go.kenn.io/roborev/internal/daemon  1.101s
```

Red, call at `ci_poller.go:1787` skipped so the range ref is kept:

```
--- FAIL: TestCISameRepoCIOptInLaunchesCursorCloud (0.24s)
    ci_cursor_cloud_ci_optin_pin_test.go:108:
        expected: "dddddddddddddddddddddddddddddddddddddddd"
        actual  : "base-dddddddddddddddddddddddddddddddddddddddd..dddddddddddddddddddddddddddddddddddddddd"
    ci_cursor_cloud_ci_optin_pin_test.go:109:
        "...base..head..." should not contain ".."
FAIL go.kenn.io/roborev/internal/daemon 0.266s
```

## EnqueueRequest.Host

Field: `internal/daemon/types.go` (`json:"host,omitempty"`).

Production writes:

- `cmd/roborev/review.go:397`, `Host: host`, where `host` is the `--host` flag and defaults to empty.

Production reads:

- `internal/daemon/server.go:3025`, `resolveEnqueueHost(..., req.Host, ...)`.
- `internal/daemon/server.go:3113`, `resolveEnqueueHost(..., in.req.Host, ...)`.

`cmd/roborev/run.go` and `internal/daemon/client.go` `EnqueueReview` build an `EnqueueRequest` and do not set `Host`. Test writes of `req.Host` are in `internal/daemon/cursor_cloud_host_test.go:196`, `:254`, `:381`, `:453` and `internal/daemon/cursor_cloud_explicit_host_pin_test.go:142`.

Verdict: reachable, and it does not launch cursor-cloud without explicit opt-in. `resolveEnqueueHost` passes the flag to `agent.ReviewHost`, which returns `cursor-cloud` only for an explicit `--host`, repo `host`, global `host`, or `ROBOREV_DEFAULT_HOST`. An empty flag does not select it.

Pin: `TestCLICursorCloudRejectsDirtyAndRangeBeforeLaunch`.

Green, unchanged tree:

```
ok  go.kenn.io/roborev/cmd/roborev  0.971s
```

Red, `Host: host` removed from the enqueue body:

```
--- FAIL: TestCLICursorCloudRejectsDirtyAndRangeBeforeLaunch (0.28s)
    cursor_cloud_cli_test.go:67:
        "...no configured agent available..." does not contain "cannot run a dirty review"
    cursor_cloud_cli_test.go:71:
        "...no configured agent available..." does not contain "cannot run a range review"
FAIL go.kenn.io/roborev/cmd/roborev 0.314s
```

## finishAgentError

Definition: `internal/daemon/worker.go:1694`.

Production callers: none. `processJob` inlines the same sentinel handling at `internal/daemon/worker.go:1441` and does not call `finishAgentError`.

Test callers:

- `internal/daemon/cursor_cloud_launch_retry_test.go:165`, `:208`, `:220`, `:250`, `:261`.
- `internal/daemon/cursor_cloud_explicit_host_pin_test.go:74`.
- `internal/daemon/cursor_cloud_retry_test.go:216`.

Verdict: unreachable from production. It does not launch cursor-cloud.

Pin: `TestExplicitHostFlagPostSendDoesNotFailOver` and `TestExplicitCursorCloudPostSendDoesNotFailOver`.

Green, unchanged tree: included in the daemon `ok` above (`1.101s`).

Red, `finishAgentError` always calls `failOrRetryAgentContext`:

```
--- FAIL: TestExplicitHostFlagPostSendDoesNotFailOver (0.17s)
    cursor_cloud_explicit_host_pin_test.go:81: expected "failed", actual "queued"
    cursor_cloud_explicit_host_pin_test.go:83: expected 0, actual 1
--- FAIL: TestExplicitCursorCloudPostSendDoesNotFailOver (0.02s)
    cursor_cloud_retry_test.go:218: expected "failed", actual "queued"
FAIL go.kenn.io/roborev/internal/daemon 0.229s
```

## refuseLocalCursorCloud and the dropped b9 check

`b9cc38be` re-pick is `0b8f9547`. Its patch-id differs because the free line has no `chosenHost`. The dropped hunk was:

```
if chosenHost == agent.CursorCloudHost {
    return errors.New("--local cannot use a cursor-cloud host")
}
```

after `agent.ReviewHost`.

On `fb536f30`, `refuseLocalCursorCloud` is `cmd/roborev/review.go:496`. It calls `agent.ReviewHost` at line 510 and refuses when that result is `cursor-cloud` at line 514.

Call sites:

- `cmd/roborev/review.go:134`, goal `--local`, `agentsToo` false. ReviewHost only.
- `cmd/roborev/review.go:208`, other `--local`, `agentsToo` true. ReviewHost, then agent name.

The ordinary site at line 208 is the one that covers the dropped check for a non-goal `--local` run. `TestLocalReviewRefusesCursorCloudHost` drives `--host`, repo `host`, and `ROBOREV_DEFAULT_HOST` through that call.

Green, unchanged tree: the `cmd/roborev` `ok` above (`0.971s`) includes `TestLocalReviewRefusesCursorCloudHost`.

Red, the call at line 208 removed. The goal call at line 134 was left in place. `TestGoalLocalReviewRefusesCursorCloudHostSources` still passed. The three ReviewHost cells went red:

```
--- FAIL: TestLocalReviewRefusesCursorCloudHost (0.03s)
    --- FAIL: TestLocalReviewRefusesCursorCloudHost/--host
        local_review_test.go:169: "...no configured agent available..." does not contain "--local cannot use a cursor-cloud host"
    --- FAIL: TestLocalReviewRefusesCursorCloudHost/.roborev.toml_host
        local_review_test.go:169: same missing refusal
    --- FAIL: TestLocalReviewRefusesCursorCloudHost/ROBOREV_DEFAULT_HOST
        local_review_test.go:169: same missing refusal
FAIL go.kenn.io/roborev/cmd/roborev 0.083s
```

## Repo host versus global host at the line 208 call

The earlier `.roborev.toml host` cell is the repo config. `writeConfig` writes
`filepath.Join(h.Dir, ".roborev.toml")` (`cmd/roborev/local_review_test.go:44`).
It does not write `config.GlobalConfigPath()`.

`TestLocalReviewRefusesCursorCloudHost` had no global `host` cell. Commit
`ca5d2487` adds one. It sets `ROBOREV_DATA_DIR` and writes
`host = "cursor-cloud"` through `config.GlobalConfigPath()`. The repo cell is
unchanged.

Green, with the `refuseLocalCursorCloud` call at `review.go:208` present
(`ROBOREV_TELEMETRY_ENABLED=0`):

```
=== RUN   TestLocalReviewRefusesCursorCloudHost
=== RUN   TestLocalReviewRefusesCursorCloudHost/--host
=== RUN   TestLocalReviewRefusesCursorCloudHost/.roborev.toml_host
=== RUN   TestLocalReviewRefusesCursorCloudHost/global_config
=== RUN   TestLocalReviewRefusesCursorCloudHost/ROBOREV_DEFAULT_HOST
--- PASS: TestLocalReviewRefusesCursorCloudHost (0.03s)
    --- PASS: TestLocalReviewRefusesCursorCloudHost/--host (0.02s)
    --- PASS: TestLocalReviewRefusesCursorCloudHost/.roborev.toml_host (0.00s)
    --- PASS: TestLocalReviewRefusesCursorCloudHost/global_config (0.00s)
    --- PASS: TestLocalReviewRefusesCursorCloudHost/ROBOREV_DEFAULT_HOST (0.00s)
PASS
ok  	go.kenn.io/roborev/cmd/roborev	0.056s
```

Red, only the call at `review.go:208` removed. The goal call at line 134 stayed.
Both config sources missed `--local cannot use a cursor-cloud host`:

```
=== RUN   TestLocalReviewRefusesCursorCloudHost/.roborev.toml_host
    local_review_test.go:177:
        Error "get agent: no configured agent available (preferred: \"codex\", backups: )..."
        does not contain "--local cannot use a cursor-cloud host"
=== RUN   TestLocalReviewRefusesCursorCloudHost/global_config
    local_review_test.go:177:
        Error "get agent: no configured agent available (preferred: \"codex\", backups: )..."
        does not contain "--local cannot use a cursor-cloud host"
--- FAIL: TestLocalReviewRefusesCursorCloudHost (0.04s)
    --- FAIL: TestLocalReviewRefusesCursorCloudHost/.roborev.toml_host (0.01s)
    --- FAIL: TestLocalReviewRefusesCursorCloudHost/global_config (0.01s)
FAIL
FAIL	go.kenn.io/roborev/cmd/roborev	0.070s
```

`--host` and `ROBOREV_DEFAULT_HOST` failed the same way in that run.
