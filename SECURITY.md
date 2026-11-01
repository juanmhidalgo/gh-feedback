# Security

Report a vulnerability privately through
[GitHub's private vulnerability reporting](https://github.com/juanmhidalgo/gh-feedback/security/advisories/new),
not in a public issue.

`gh-feedback` uses the token `gh` is logged in with and never stores it. It
writes to GitHub only from `resolve`, `dismiss` and `react`. Comment bodies are
untrusted text written by other people and bots: `fetch --split` exists so a
caller, such as a coding agent, can keep them out of its own context.
