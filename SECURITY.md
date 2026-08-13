# Security Policy — Archerik

## Reporting a vulnerability

Please report security issues **privately**. Do not open a public issue.

Use [GitHub's private vulnerability reporting][advisory] on this repository, or
email <farhadamjadytoosi@gmail.com> with the details.

[advisory]: https://github.com/farhadamjady/service-discovery/security/advisories/new

Please include what you did, what happened, and what you expected — a minimal
reproducing repository or file is the most useful thing you can send. You can
expect an initial response within a week.

## Supported versions

The latest release on the default branch is supported. Fixes are not
backported.

## What this tool touches

Understanding the trust boundary helps when judging whether something is a
vulnerability:

- **It reads source code.** The extractor parses a repository's files with
  tree-sitter and reads its configuration. It never **executes**, builds, or
  evaluates the scanned code, and it never renders Helm or Kustomize templates
  — deployment config is parsed as static text.
- **A local run sends nothing.** With no `--api-url`, the extractor is entirely
  offline: it reads the repo and writes JSON.
- **Only derived JSON leaves.** When `--api-url` is set, the extractor submits
  the architecture graph — endpoint paths, dependency targets, topic names, and
  declared type structures. It does not upload source files.
- **Secrets.** The API key is read from `--api-key`, `EKG_API_KEY`, or a config
  file, and is never logged; a test pins that it appears in neither stdout nor
  stderr. Note that a scanned repository's *config values* may themselves
  contain secrets, and resolved values can appear in the output graph — treat
  the output with the same care as the repository it came from.

Things that are **not** vulnerabilities in this project:

- Bypassing the startup API-key check in a local build. Extraction is free and
  the check is not a security boundary; the enforcement point is the
  server-side re-validation at submit.
- The extractor producing an incorrect or incomplete graph. That is a
  correctness bug — please open a normal issue.

Reports involving parser crashes, unbounded resource use, or path traversal on
a hostile input repository are in scope and welcome.
