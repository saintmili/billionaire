# Recoverable agent work

Before creating implementation worktrees or running project verification, use
the repository tools below. They retain state under the main checkout, including
when invoked from a worktree. Requires Git, Python 3, Go and Docker Compose.

## Workspaces

```sh
./scripts/workspace create task-name --ticket ISSUE_REF --base INTEGRATION_BRANCH --blocked-by BLOCKER_REF
./scripts/workspace status task-name
./scripts/workspace checkpoint task-name --note 'Spec: docs/example.md; next: remaining acceptance check'
```

Replace the reference placeholders with the current ticket, integration branch
and dependencies. Creating a workspace records a dependency; it does not authorize
starting a blocked ticket. Verify tracker blockers before implementation.

The branch defaults to `agent/<name>`. Files live in `.worktrees/<name>` and the
ledger in `.scratch/runs/<name>/state.json`; both locations are gitignored.
Put additional context pointers in that run directory. The ledger captures ticket,
dependencies, base/current commit, current source fingerprint, dirty state and
matching verification. It stores no database URL or environment credentials.

Checkpoint after a meaningful slice and before handing off. Commit completed
code before handoff; `checkpoint` records work and never stages, commits or resets
it. Uncommitted files survive session interruption in the persistent worktree,
but only committed work can be rebuilt after the directory itself is deleted.

After an interruption, run `workspace status` to inspect all ledgers, then
`workspace recover NAME` if a directory is missing. Recovery uses the existing
branch, or the checkpoint commit if the branch was removed. It prunes stale Git
worktree registrations and recreates the missing directory; existing directories
with an unexpected branch/path are refused. It never resets a branch.

After merging and verifying a task, remove its clean worktree with
`git worktree remove .worktrees/NAME`. Keep the small ledger as a recovery pointer.

## Verification

```sh
./scripts/check --status
./scripts/check
```

`check` is the local and CI entry point. It runs the workflow-tool tests, provisions
the pinned PostgreSQL image from `compose.test.yaml` on an ephemeral loopback port,
checks database/role creation and restricted-role login, then runs the full Go
race suite, vet and build. It ignores caller `BILLIONAIRE_*` configuration and uses
its own disposable server. Required integration coverage fails rather than skips
if its DSN is absent. Ordinary standalone `go test` retains opt-in database tests.

The runner removes its unique Compose project and volumes on success, failure or
handled interruption. Its project name is recorded for cleanup after a hard kill.
Trust authentication is limited to this disposable test service; the application's
normal Compose configuration is unchanged.

Results and streamed logs remain in `.scratch/checks/<run>/`. Records include the
profile, toolchain, actual PostgreSQL image, source commit/fingerprint, dirty state,
commands and outcomes. The fingerprint includes tracked and untracked source,
deletions and executable bits, while ignored workspaces/logs are excluded. A run
that observes changed source finishes `stale`, never `passed`. Leave the checked
worktree stable while checks run; other task worktrees can proceed independently.

Use targeted tests during red/green work. Before repeating broad checks, inspect
the existing record for the same source/toolchain/profile. A final full run covers
the assembled source; additional source changes or unresolved failures justify
another run. `--status` reports evidence and never skips a requested check.

Review uses the judgment questions in root `CODING_STANDARDS.md`, discovered by
the code-review skill. Keep workflow detail here; `AGENTS.md` only points to it.
