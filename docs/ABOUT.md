# ABOUT

Short onboarding doc for newcomers: what the project does, how it works, and
which technologies it uses.

## What the utility does

`gdrive-migrate` copies a selected subfolder from one Google Drive location to
another.

In simple terms:
- there is a source root (`source_folder_id`);
- there is a target root (`target_folder_id`);
- there is a subfolder to migrate (`sub_folder` or `sub_folder_id`);
- the utility copies that subtree into target.

## How it works (very short)

1. Reads `config.yaml` and CLI flags.
2. Performs OAuth authorization (Google account).
3. Resolves the requested source subfolder.
4. Depending on mode:
   - `estimate_only` -> only estimates size/count and exits;
   - `skip_scan=false` -> scans source tree and builds a plan first;
   - `skip_scan=true` -> starts direct copy without pre-flight plan.
5. Creates or resolves root folder in target.
6. Copies files server-side using multiple workers.
7. Writes progress to `manifest.jsonl` for resume after interruption.

## Main code locations

- `main.go` - startup, CLI flags, pipeline launch.
- `internal/config` - config loading and validation.
- `internal/auth` - OAuth login and token handling.
- `internal/drive` - Google Drive API integration:
  - `client.go` - API calls;
  - `scanner.go` - scan and estimate;
  - `planner.go` - folder structure creation;
  - `copier.go` - file copy.
- `internal/manifest` - resume via `manifest.jsonl`.
- `internal/pipeline` - end-to-end business flow.

## Important invariants

- Resume: if file is already `done` in manifest, do not copy it again.
- Target root: each job must use the correct target folder.
- Mode behavior:
  - `estimate_only` must not modify target;
  - `dry_run` must not copy files;
  - `skip_scan=true` must not run pre-flight scan.

## What is Google Drive API

Google Drive API is Google's official HTTP API for programmatic file/folder
operations in Drive.

With this API you can:
- read file and folder metadata;
- create folders;
- copy files;
- search by name/parent;
- work with Shared Drives (with appropriate API flags).

This project uses:
- `google.golang.org/api/drive/v3`

## What is server-side copy

Server-side copy means copying within Google's infrastructure, without
downloading files to your machine and uploading them back.

Benefits:
- usually faster;
- less traffic through local machine;
- more stable for large datasets.

Limitations:
- access is required to both source and target;
- Google API quotas still apply;
- rare file/domain restrictions may affect migration.

## Where to continue

- For setup and usage examples: `README.md`.
- For technical details and invariants: `docs/AGENT_HANDOFF.md`.
