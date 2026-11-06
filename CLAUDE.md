# CLAUDE.md

## Agent skills

### Issue tracker

Issues are tracked in GitHub Issues for jimcru21/pantalan, using the `gh` CLI. See `docs/agents/issue-tracker.md`.

### Triage labels

Uses the five default labels: needs-triage, needs-info, ready-for-agent, ready-for-human, wontfix. See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: one `GLOSSARY.md` and `docs/adr/` at the repo root. See `docs/agents/domain.md`.

## Commits

Commit titles follow [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(<scope>): <summary>
```

- **type**: `feat`, `fix`, `refactor`, `perf`, `test`, `docs`, `build`, `ci`, `chore`, `revert`.
- **scope** (optional): the area touched, usually the package under `internal/` (`proxy`, `queue`, `store`, ...), or `web`, `cmd`. Omit it when the change spans several areas.
- **summary**: imperative mood, lowercase, no trailing period, whole title ≤ 72 characters.
- **Breaking changes**: add `!` after the type/scope (`feat(httpapi)!: ...`) and a `BREAKING CHANGE:` footer.
- **Body**: blank line after the title, wrapped at 72 columns, explaining why the change exists, not restating the diff.
- Branch names use the same type as a prefix: `<type>/<short-description>` (e.g. `chore/agent-skills-setup`).

Examples:

```
feat(proxy): route requests by Host header
fix(queue): stop retrying jobs after the context is cancelled
docs: describe the webhook secret in the README
```
