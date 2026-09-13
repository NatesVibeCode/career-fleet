# Security Policy

## Supported version

Security fixes are applied to the latest release on the default branch.

## Report a vulnerability

Use GitHub's [private vulnerability reporting](https://github.com/NatesVibeCode/career-fleet/security/advisories/new). Do not open a public issue for an undisclosed vulnerability or include credentials, private source text, or provider responses in a report.

Include the affected version, operating system, reproduction steps, expected result, actual result, and impact. Maintainers will acknowledge a complete report within seven days.

## Data boundary

Career Fleet's discovery and scoring commands store captured source text, job metadata, evaluation quotes, and exported dossiers locally. Protect SQLite files, profiles, and exports according to the sensitivity of the candidate and source data they contain. The default `career_fleet.db`, `profile.json`, and `qualified_targets*.json` files are ignored by the repository; custom output paths are not automatically ignored. The deterministic career lanes do not require a model provider.
