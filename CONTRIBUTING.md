# Contributing to YACM

Thanks for helping. YACM is free software under the GNU GPL version 3 or
later with the additional terms in NOTICE.md, and it stays free and open
source.

## Reporting bugs and ideas

Open an issue with:

- the YACM version (Settings > About) and your operating system;
- what you did, what you expected and what happened;
- a screenshot if it helps.

Never paste passwords, keys, vault files or device configurations that contain
secrets.

Security problems do not go into issues: see SECURITY.md.

## Code

1. Build the frontend first (`cd frontend && npm ci && npm run build`), then
   run `gofmt -l .`, `go vet ./...` and `go test ./...`. The build steps are
   in docs/development.md.
2. Keep changes focused: one fix or feature per pull request. Large pull
   requests are split up or declined.
3. New functionality and bug fixes come with automated tests: Go tests for
   the program, Playwright tests for changes in the user interface
   (docs/development.md says how to run them). CI runs them on every pull
   request.
4. User-facing text is in English and short.
5. Between 0.9 and 1.0 YACM takes fixes only, no new features.

## How changes get in

YACM handles passwords and logs in to network devices, so every change is
reviewed with that in mind:

- The project has one maintainer, who merges every pull request; nobody else
  gets write access to the repository.
- Changes to the workflows in `.github`, the build scripts, `go.mod`,
  `go.sum`, `frontend/package.json` and its lock file, the vault and
  cryptography, the importers and the network code get the closest look.
- A new dependency needs a clear reason and a permissive licence (MIT, BSD,
  Apache 2.0, ISC and the like); `tools/licensecheck` checks the Go modules.
- Workflows from first-time contributors run only after the maintainer
  approves them, and releases are built only by GitHub Actions in this
  repository, with checksums, an SBOM and build provenance.

## Contributor License Agreement

Before your first pull request can be merged, you agree to the YACM
Contributor License Agreement (CLA.md), once: the CLA Assistant bot asks for
it in a comment on the pull request. In short:

- you keep the copyright in your contribution;
- you allow the YACM maintainer named in CLA.md to distribute it as part of
  YACM, also under licences other than the GPL;
- your contribution always stays available under the licence YACM uses on
  the day you contribute (CLA section 2.3), so YACM stays free and open;
- if you contribute as part of your job, your employer agrees to it
  (CLA section 3c): either in writing, or by signing the Entity Contributor
  License Agreement (CLA-ENTITY.md) and sending it to support@yacm.app.

Both agreements are based on the Harmony agreements (HA-CLA-I-ANY and
HA-CLA-E-ANY), standard templates; YACM's names, the governing law (Croatia)
and a section on signing were filled in or added.

## Work made with AI assistants

Much of YACM is written by its maintainer with an AI coding assistant
(Claude, by Anthropic), under the maintainer's direction and review. Commits
made that way may name the assistant as author or co-author; they are the
maintainer's work, and the maintainer holds whatever rights exist in them.
Contributors may use such tools too: you are responsible for what you submit,
as for any other contribution, and the CLA applies to it in the same way.

New source files start with the same two lines as the others:

    // SPDX-License-Identifier: GPL-3.0-or-later
    // Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.
