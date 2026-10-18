# offpeak: agent guide

This guide is for AI coding agents and for people. It uses ASD-STE100 Simplified Technical English (STE).

## Language

- Write docs, comments, commit messages and log messages in STE.
- Write short sentences (20 words or less). Use the active voice. Write one instruction per sentence.
- Do not use the em dash. Use a plain dash ("-").

## Comments

- Keep comments short: one or two sentences.
- Do not write blocks of comments. Do not repeat what the code says.
- Write why the code does something, if that is not clear from the code.
- Start a doc comment with the name of the item, as in Effective Go.

## Project layout

- This is a Kubebuilder v4 project. Use the `kubebuilder` CLI to scaffold. Do not move scaffold files.
- Do not remove `+kubebuilder:scaffold` markers.
- Do not edit generated files: `zz_generated.*`, `config/crd/bases/*`, `config/rbac/role.yaml`, `PROJECT`.
- The design is in `docs/DESIGN.md`. Record each design change in `docs/DECISIONS.md` in the same commit.

## After a change

1. After a change to `api/`, run `make manifests generate`.
2. After a CRD change, run `kubebuilder edit --plugins=helm/v2-alpha`, then `make check-chart`.
3. Run `make test lint vulncheck`. After a change to a workflow, run `make lint-actions`.
4. After a change to the controller or the chart, run `make test-e2e` or `make test-chart`. They use a dedicated Kind cluster.

## Code

- Write the test first. Use table tests for pure logic and envtest for the controller.
- Return wrapped errors: `fmt.Errorf("...: %w", err)`.
- Never log a token or a URL that has a token.
- Keep files below 400 lines and functions short.

## Dependencies and security

- Pin exact versions. Commit `go.sum`. Keep `GOSUMDB` on.
- Pin images by digest and GitHub Actions by commit SHA.
- Pin tools in the `Makefile`, and check downloaded binaries with SHA-256.
- Prefer the standard library. Add a module only when it clearly pays off.
