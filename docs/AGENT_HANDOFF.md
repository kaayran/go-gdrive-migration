# AGENT_HANDOFF

Technical handoff doc for the next agent/developer.
Goal: quickly understand architecture, invariants, and safe change points.

## 1) Project Context

- Project: `go-gdrive-migration`
- Language: Go
- Scenario: copy subtree(s) from `source_folder_id` to `target_folder_id` in Google Drive using `sub_folder` or one/multiple `sub_folder_id` values.
- Copy mode: server-side only (`Drive API files.copy`), no local download/upload.
- Key goals:
  - high throughput (parallelism + server-side copy),
  - pre-flight stats (folders/files/bytes before copy),
  - skip empty folders,
  - reliable resume via `manifest.jsonl`.

## 2) Architecture by Package

- `main.go`
  - parses CLI flags (`--config`, `--yes`, `--dry-run`, `--no-resume`, `--version`);
  - loads config;
  - creates context with graceful Ctrl+C handling;
  - runs `pipeline.Run()`.

- `internal/config`
  - YAML config load/validation;
  - defaults;
  - normalization for `source_folder_id` / `target_folder_id` (raw IDs and folder URLs supported);
  - resolves relative file paths (`credentials`, `token`, `manifest`) relative to config dir.

- `internal/auth`
  - OAuth flow:
    - reuses `token.json` if present;
    - otherwise starts local callback server (`127.0.0.1:<random>/callback`) and opens browser;
    - saves new token into `token.json`.

- `internal/drive`
  - `client.go`:
    - wrapper around `drive.Service`;
    - API methods: `ResolveSubFolder`, `GetFile`, `ListChildren`, `CreateFolder`, `CopyFile`;
    - retry logic with exponential backoff + jitter.
  - `scanner.go`:
    - parallel BFS over source tree;
    - builds `ScanResult` (folders/files/bytes);
    - computes `TotalFiles` per subtree (for skip-empty logic).
  - `planner.go`:
    - creates mirrored folder tree in target;
    - returns mapping `sourceFolderID -> targetFolderID`.
  - `copier.go`:
    - worker pool for server-side file copy;
    - supports skipping already copied files via `shouldSkip` callback.

- `internal/manifest`
  - append-only JSONL;
  - stores:
    - successful copies (`status=done`) for resume;
    - service record `status=target_root` (target root folder ID).

- `internal/progress`
  - progress bars/spinners (`schollz/progressbar`):
    - scan spinner,
    - plan bar,
    - copy bars (files + bytes).

- `internal/pipeline`
  - orchestrates all stages.

## 3) Runtime Pipeline (Current)

`pipeline.Run()` stages:

1. `[1/6] Authenticating`
   - OAuth and Drive client init.

2. `[2/6] Resolving source sub-folder`
   - resolves `sub_folder` path or uses `sub_folder_id`.
   - if multiple IDs are provided (`,` / `;` / newline), they are processed as sequential jobs.

3. `[3/6] Scanning source tree`
   - scans nested folders/files and gathers pre-flight stats.
   - if `options.skip_scan=true`, this stage is skipped (direct-copy mode).
   - if `options.estimate_only=true` (or CLI `--estimate`), performs lightweight estimate and exits without target changes.

4. `Plan summary`
   - displays copy plan.
   - `--dry-run` exits here.

5. `[4/6] Preparing target sub-folder`
   - get-or-create target root for current job:
     - first from `manifest.target_root` (if still exists),
     - else search by source sub-folder name in `target_folder_id`,
     - else create;
   - persists ID to manifest (`status=target_root`).

6. `[5/6] Creating target folder structure`
   - creates folders under target root;
   - empty subtrees are skipped when `skip_empty_folders=true`.
   - in direct-copy mode (`skip_scan=true`), folders are created on-the-fly (no separate planning stage).

7. `[6/6] Copying files (server-side)`
   - builds copy tasks;
   - runs worker pool;
   - writes per-file results to `manifest.jsonl`.

## 4) Critical Invariants

1. **Resume is based on `src_id` + `status=done`**
   - files already marked `done` must not be recopied.

2. **Target root must stay stable across restarts**
   - enforced by persisting `status=target_root` in manifest.

3. **`sub_folder` must map to a dedicated target folder**
   - copy is not done directly into `target_folder_id`.

4. **Skip-empty relies on `FolderNode.TotalFiles`**
   - do not break `computeSubtreeCounts()`.
   - with `skip_scan=true`, skip-empty is not applied.

5. **Copy mode is server-side only**
   - no download/upload fallback implemented.

## 5) Manifest Format

File: `manifest.jsonl` (append-only, one JSON per line).

Statuses:
- `done` - file copied successfully,
- `failed` - copy error,
- `skipped` - skipped (for example already done),
- `target_root` - service record with target root folder ID.

On startup `loadCompleted()` restores:
- `done map[src_id]bool` for current scope,
- `targetRoot` for current scope.

Scope = current `sub_folder_id`, preventing cross-job reuse issues after scope changes.

## 6) Common Problems

### Folder created but files are not copied

Likely cause: existing manifest already has many `status=done` for those `src_id`.

Fix:
- delete `manifest.jsonl`, or
- run with `--no-resume`.

### Duplicate folders in target

`resolveOrCreateTargetRoot()` searches target root by name.
If more than one match is found, pipeline fails explicitly.
Duplicates must be cleaned manually.

### OAuth blocked in organization

Usually Google Workspace policy:
- app access control,
- restricted scopes,
- internal/external app restrictions.

## 7) Typical Change Points

### Add file filtering (extension/size/name)

Touch:
- task construction in `internal/pipeline/pipeline.go`,
- optionally `Config.Options`.

### Add post-copy checksum verify

Touch:
- use `SrcMD5` / `DstMD5` in pipeline `copier.OnResult`,
- add final verify stage after copy.

### Add download/upload fallback

Touch:
- add new method in `internal/drive/client.go`,
- add mode selection strategy in pipeline.

### Strengthen folder idempotency rules

Touch:
- `resolveOrCreateTargetRoot()` in pipeline,
- optionally move lookup/create behavior into `drive.Client` with stricter metadata matching.

## 8) Build and Run (Short)

- Config: `config.yaml` (template: `config.example.yaml`).
- Build:
  - `go mod tidy`
  - `go build -o go-gdrive-migration.exe .`
  - or `build.ps1` for cross-platform builds.

Minimal run:
- `./go-gdrive-migration --config config.yaml`

## 9) Current Risks

1. Very large migrations can produce very large manifest files.
2. `verify_checksums` option exists, but full verify stage is not implemented yet.
3. No unit/integration tests yet.
4. No global rate limiter, only retry-on-error strategy.
5. UX can be tricky when target already has partially copied structure.

## 10) Recommended Next Steps

1. Add tests for:
   - `resolveOrCreateTargetRoot`,
   - `manifest.loadCompleted`,
   - `computeSubtreeCounts`.
2. Implement optional `verify_checksums`.
3. Add include/exclude filters (extensions, max size).
4. Extend final report (done/failed/skipped totals).
5. Add explicit modes for "strict resume" and "fresh run".

---

For any non-trivial change, start with:
- `internal/pipeline/pipeline.go` (orchestration and business rules),
- `internal/drive/*` (core algorithms),
- `internal/manifest/manifest.go` (idempotency/resume behavior).
