# Changelog

All notable changes to this project are listed here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
This project uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.1.0] - 2026-11-02

### Added

- CLI commands: `list`, `get`, `create`, `update`, `enable`, `disable`, `delete`, `completion`.
- Interactive mode. It runs inline in the console and redraws one fixed region.
- Output formats `table`, `wide` and `json` (`-o`, or the `--json` shorthand). Interactive mode switches between table and JSON with `j`.
- Key lookup by full hash, unique hash prefix or exact name.
- Pagination across all pages, and filters for disabled keys and workspace.
- Expiry input as a duration (`30d`), a date or RFC 3339.
- GET requests retry on HTTP 429 and 5xx.

[Unreleased]: https://github.com/hatedabamboo/orkeys/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/hatedabamboo/orkeys/releases/tag/v0.1.0
