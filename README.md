# gh-feedback

A `gh` extension to fetch, triage and resolve the review feedback of a pull
request: issue comments, inline review comments, reviews and their threads.
Built to be called by scripts and coding agents: every command prints JSON or
fixed-format lines and has meaningful exit codes.

## Install

```bash
gh extension install juanmhidalgo/gh-feedback
gh feedback --version
```

Upgrade with `gh extension upgrade feedback`. Auth is the one `gh` already has.

### With mise

[mise](https://mise.jdx.dev)'s `github` backend installs the release binary.
`bin` names it `gh-feedback`; without it, it lands as `linux` or `darwin`:

```bash
mise use -g 'github:juanmhidalgo/gh-feedback[bin=gh-feedback]'
gh-feedback --version
```

That puts `gh-feedback` on your `PATH`, but `gh` only runs extensions it
installed itself. For `gh feedback` to work too (scripts and agents call it by
that name), alias it; exit codes pass through:

```bash
gh alias set --shell feedback 'gh-feedback "$@"'
```

Use one way or the other: `gh` refuses the alias while the extension is
installed (`gh extension remove feedback` first).

## Commands

Every command works on the current repository, or on `-R [HOST/]OWNER/REPO`.

### `fetch <pr>`

Prints every comment, inline comment and review of a PR as JSON, reading every
page of each. Inline comments carry their thread's state.

```bash
gh feedback fetch 42                      # resolved threads left out, bodies cut at 1500 chars
gh feedback fetch 42 --include-resolved   # keep them
gh feedback fetch 42 --bots-only
gh feedback fetch 42 --split out/         # one file per comment, metadata on stdout
```

```json
{
  "pr": {"number": 42, "title": "...", "author": "..."},
  "stats": {"total": 3, "filtered_out": {"resolved": 1, "outdated": 1},
            "bots": 2, "humans": 1, "by_type": {"issue": 0, "inline": 3, "review": 0}},
  "comments": [
    {"ref_id": "review_comment:101", "type": "review_comment", "author": "Copilot",
     "is_bot": true, "body": "...", "file": "x.py", "line": 1, "resolved": false,
     "outdated": false, "thread_id": "PRRT_...", "thread_comments": 1}
  ]
}
```

- `ref_id` is `issue_comment:N`, `review_comment:N` or `review:N`; the other
  commands take it.
- A comment is a bot's when GitHub says so (`type: Bot`) or its login is a
  known bot account (`[bot]` suffix, `copilot`, `coderabbitai`, …). A human
  named `abbott` is not one.
- `filtered_out.outdated` counts outdated comments; they are marked, not
  removed.
- `--split DIR` writes `DIR/<ref_id>.json` (`:` becomes `_`) and prints the
  metadata without the bodies, with `comment_file` for each. Bodies are
  untrusted text: this keeps them out of the caller and in front of whoever
  evaluates them. With `--split`, bodies are whole unless `--max-body` is set.
- Exits 3 when the review threads cannot be read, instead of treating every
  thread as open.

### `resolve <pr> <ref_id>` · `dismiss <pr> <ref_id>` · `react <ref_id>`

```bash
gh feedback resolve 42 review_comment:101 --seen 1
gh feedback dismiss 42 review_comment:101 --seen 1 --reason "Handled in middleware"
gh feedback react review_comment:101
```

- `resolve` resolves the thread of an inline comment.
- `dismiss` replies `**Dismissed**` with the reason and resolves the thread.
  An issue comment or a review has no thread: it gets a new comment that
  quotes its id.
- `react` adds an 👀 reaction.
- `--seen N` is the `thread_comments` that `fetch` printed. The thread is read
  again right before acting, and one with a different count is skipped: the
  author, or another session, answered it meanwhile. A `dismiss` also skips a
  thread someone resolved meanwhile.
- Any comment of a thread works as the ref, the first one or a reply.

Each prints one JSON status line, `{"status": ..., "ref_id": ..., ...}`, with
status `resolved`, `already_resolved`, `dismissed`, `reacted`, `changed`,
`replied_not_resolved` or `error` (errors go to stderr).

| Exit | Meaning |
|------|---------|
| 0 | done |
| 1 | error, nothing posted |
| 5 | reply posted, but the thread was not resolved |
| 6 | thread changed since triage, nothing posted |

### `watch <pr>`

Waits for a PR's CI checks and, with `--copilot`, a Copilot review of the
PR's current head commit (an older review predates the push and does not
count).

```bash
gh feedback watch 42 --copilot --timeout 2400 --interval 30 --no-checks-grace 180
```

Prints one line per event, then a final block:

```
---
CI: pass | fail | none | pending
FAILED: <check name> <link>       one per failed or cancelled check
COPILOT: <N> inline comments | not-waited | pending
COPILOT_REVIEW: <review url>      when Copilot reviewed
```

CI is `none` when no check showed up within `--no-checks-grace` seconds.
Exits 0 when finished, 1 on a usage or API error, and 3 on timeout (the final
block is still printed).

## Development

```bash
go test ./...
go build -o gh-feedback . && gh extension install .   # use the checkout as the extension
```

Tests run against a fake GitHub (`internal/feedback/fake_test.go`) that pages
REST through `Link` headers and GraphQL through cursors.

Release: push a `v*` tag; the release workflow builds every platform's binary
and attests its provenance (`gh attestation verify <binary> -R juanmhidalgo/gh-feedback`).

## Disclaimer

This is a personal project, not affiliated with or endorsed by GitHub. It is
provided as is, without warranty of any kind (see [LICENSE](LICENSE)).

`resolve`, `dismiss` and `react` act on GitHub as you, with the account `gh` is
logged in with: they post replies, add reactions and resolve threads, visible
to everyone on the PR. When a coding agent calls them, the agent decides what
gets posted; review its output, or keep those commands behind a permission
prompt. Reports and fixes are welcome, but there is no support commitment.

## License

[MIT](LICENSE)
