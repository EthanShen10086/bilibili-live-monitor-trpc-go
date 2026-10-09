# Repository instructions

- Read CONTRIBUTING.md before changing code or development tooling.
- Preserve unrelated working-tree changes. Separate formatting, behavior fixes and tool configuration commits.
- Run make fmt-check, make lint and relevant tests. CI must use the same scripts/quality.py entry point.
- Add behavior tests for state, retry, cancellation, durability and error handling changes. Real backends require the integration tag and disposable endpoints.
- Never use production credentials, send real notifications, restart the user's service, push or deploy as part of ordinary tests.
- Do not edit applied migrations, bypass hooks, override global/company hook chains or add unexplained nolint exclusions.
- Use package boundaries and consumer interfaces documented in CONTRIBUTING.md; avoid redundant facades or speculative infrastructure.
- Report unit tests, backend integration, process smoke, real delivery, commit, push and deployment as separate facts.
