# gears-cli

The Gears CLI: the local door to a Gears project. Binds a directory to a project, keeps shaped artifacts beside the code, runs the desk and session log, drives runbooks, and serves every verb as an MCP tool over stdio (`gears run`).

Binary name is `gears`. Module `github.com/Syluxso/gears-cli`.

Install (the binary name differs from the module name, so `go install` alone
would produce `gears-cli`):

    go build -o "$(go env GOPATH)/bin/gears" .

Then in any repo: `gears init`, `gears status`. For an MCP harness, point it at
`gears run --dir <repo>` with `GEARS_AGENT` set to the agent's label.

Sibling repos:

- [gears-api](https://github.com/Syluxso/gears-api) — Go / Gin / byz-platform
- [gears-app](https://github.com/Syluxso/gears-app) — post-auth Angular app
- [gears-front](https://github.com/Syluxso/gears-front) — pre-auth Next.js site

Earlier generations of Gears live in `Syluxso/gears`; nothing from them is carried over.
