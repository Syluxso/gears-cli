# gears-cli

The Gears CLI: the local door to a Gears project. Binds a directory to a project, keeps shaped artifacts beside the code, runs the desk and session log, drives runbooks, and serves every verb as an MCP tool over stdio (`gears run`).

Binary name is `gears`. Module `github.com/Syluxso/gears-cli`.

Install (the binary name differs from the module name, so `go install` alone
would produce `gears-cli`):

    go build -o "$(go env GOPATH)/bin/gears" .

Then at a workspace root (the directory holding `projects/<repo>`): `gears init`,
`gears status`. The root is the only `.gears/`; init records the git repos
under `projects/` in `.gears/projects.yaml` (`gears projects scan` to refresh,
`gears projects list` to print). Commands run inside `projects/<repo>` resolve
to the root and tag session entries and new artifacts with that project;
`gears init` there is refused. A standalone clone with no workspace above it
still runs `gears init` on its own. For an MCP harness, point it at
`gears run --dir <workspace root>` with `GEARS_AGENT` set to the agent's label.

Account and sync (slice 1):

    gears login --email you@example.com      # device token to ~/.gears/credentials.json,
                                             # then registers this workspace/project, reports pulls
    gears sync                               # push desk, session, artifact changes; pull and apply
    gears whoami                             # host, token state, workspace this syncs to
    gears logout                             # revoke the device, forget the token

`GEARS_API` or `--api` picks the host (default api.gearsos.com). `.gears/` is
never committed: project.yaml is the binding, the rest is cache; the record is
gearsos.com. A fresh clone binds by its git remote on first login.
`scripts/e2e-sync.sh` proves the round trip against a local gears-api.

Sibling repos:

- [gears-api](https://github.com/Syluxso/gears-api) — Go / Gin / byz-platform
- [gears-app](https://github.com/Syluxso/gears-app) — post-auth Angular app
- [gears-front](https://github.com/Syluxso/gears-front) — pre-auth Next.js site

Earlier generations of Gears live in `Syluxso/gears`; nothing from them is carried over.
