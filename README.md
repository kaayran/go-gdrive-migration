# go-gdrive-migration

Russian docs: [`docs/README.ru.md`](./docs/README.ru.md)

Utility for fast folder and file copy between two Google Drive folders that are
accessible by the same account. Uses **server-side copy** via Google Drive API,
so files do not pass through your machine.

## Features

- Single static binary, no runtime dependencies (Windows / Linux / macOS).
- Parallel tree traversal and parallel file copy (configurable).
- Optional pre-flight scan: collect stats first, or skip scan and copy directly.
- Real-time progress bars for files and bytes.
- Skip empty folders (including empty subtrees).
- Resume after interruption with `manifest.jsonl`.
- Retry with exponential backoff for rate-limit and 5xx errors.
- Dry-run mode (show plan only).
- Per-job run report with copied files/bytes and duration.
- Run reports are saved to `reports/run-report-YYYYMMDD-HHMMSS.txt`.
- YAML-based configuration.

---

## Quick Start

### 1) Install Go

Download from [go.dev/dl](https://go.dev/dl) and verify:

```powershell
go version
```

### 2) Create OAuth Client ID

One-time setup. One `credentials.json` can be reused across machines/jobs.

1. Open [Google Cloud Console](https://console.cloud.google.com).
2. Create/select a project.
3. **APIs & Services -> Library -> Google Drive API -> Enable**.
4. **APIs & Services -> OAuth consent screen**:
   - User Type: External (personal Gmail) or Internal (Workspace).
   - Fill required fields (app name/support email).
   - Add scope: `https://www.googleapis.com/auth/drive`.
   - Add your email in Test users.
5. **APIs & Services -> Credentials -> Create Credentials -> OAuth client ID**:
   - Application type: **Desktop app**.
   - Download JSON and rename it to `credentials.json`.

### 3) Build

```powershell
cd D:\Projects\go-gdrive-migration
go mod tidy
.\build.ps1 -Target win
```

Binary output: `.\dist\go-gdrive-migration.exe`.

### 4) Configure

```powershell
Copy-Item config.example.yaml config.yaml
notepad config.yaml
```

Fill:

- `source_folder_id` - source root folder (ID or URL).
- `target_folder_id` - destination root folder (ID or URL).
- `sub_folder` - one or multiple paths (` , ` / `;` / newline), **or**
- `sub_folder_id` - one or multiple folder IDs (` , ` / `;` / newline).
- `options.target_subfolder_postfix` - optional suffix for target subfolder name.

Folder ID is the segment after `/folders/` in a Drive URL.

### 5) Place files together and run

```text
go-gdrive-migration\
├── go-gdrive-migration.exe
├── credentials.json
└── config.yaml
```

```powershell
.\dist\go-gdrive-migration.exe --config config.yaml
```

First run opens a browser for OAuth login. `token.json` is then stored locally
and reused on next runs.

---

## CLI Flags

```text
--config <path>   path to config.yaml (default: ./config.yaml)
--sub-folder      override sub_folder from config (path or list)
--sub-folder-id   override sub_folder_id from config (ID or list)
--target-subfolder-postfix  override options.target_subfolder_postfix
--yes             skip copy confirmation prompts (CI-friendly)
--dry-run         no copy (with skip_scan=false shows scan+plan)
--estimate        quick folders/files/bytes estimate and exit
--no-resume       ignore existing manifest and start from scratch
--version         show version
```

Priority for source selection: `--sub-folder-id` > `--sub-folder` > `config.yaml`.
Priority for postfix: `--target-subfolder-postfix` > `options.target_subfolder_postfix`.

Examples:

```powershell
.\dist\go-gdrive-migration.exe --config config.yaml --sub-folder "Folder1,Folder2" --estimate
.\dist\go-gdrive-migration.exe --config config.yaml --sub-folder-id "1AAA...,1BBB..." --yes
.\dist\go-gdrive-migration.exe --config config.yaml --sub-folder "MyFolder" --target-subfolder-postfix " Promo Materials"
```

---

## How It Works

```text
[1/6] Auth         -> OAuth flow (first run only)
[2/6] Resolve src  -> resolve sub_folder path under source_folder_id,
                      or use sub_folder_id directly
[3/6] Scan         -> if options.skip_scan=false: parallel scan and stats
[4/6] Prepare tgt  -> create/find target root folder for this job
[5/6] Plan         -> create mirrored folder structure in target
[6/6] Copy         -> server-side copy with retries + manifest resume
```

### Pre-flight Scan Modes

`options.skip_scan`:

- `false` - standard mode: scan first, then copy.
- `true` - direct-copy mode: skip pre-flight scan and start copy immediately.

Notes:

- with `dry_run: true` and `skip_scan: true`, no scan and no copy (no changes).
- with `skip_scan: true`, `skip_empty_folders` is ignored.
- keep `verify_checksums: false` for faster scan and lower metadata payload.

### Estimate Mode

Use `--estimate` or `options.estimate_only: true` for quick sizing only:

- counts folders/files/bytes;
- does not create target folders;
- does not copy files;
- does not use manifest/resume.

### Run Reports

When copy runs, a report is written to:

- `reports/run-report-YYYYMMDD-HHMMSS.txt`

For multiple `sub_folder` / `sub_folder_id` inputs, report includes a block per job.

---

## Resume and `manifest.jsonl`

Each processed file appends one JSON line:

```json
{"src_id":"abc","dst_id":"xyz","path":"Assets/img.png","size":12345,"status":"done","ts":"2026-04-26T18:00:00Z"}
{"src_id":"def","path":"Assets/big.psd","status":"failed","error":"...","ts":"..."}
```

On rerun with resume enabled (default), successful `src_id` values are skipped.
Failed entries are retried. To restart from scratch, remove `manifest.jsonl` or
run with `--no-resume`.

---

## Google Drive API Limits

Typical defaults:

- ~1000 requests / 100 seconds / user
- ~10,000 requests / 100 seconds / project

Tool retries 429 and rate-limit 403 automatically with exponential backoff.
If throttling is frequent, reduce `copy_workers` (for example to 6-8).

---

## Security

- `credentials.json` and `token.json` are in `.gitignore`.
- Token is stored locally only.
- OAuth callback server is temporary and bound to `127.0.0.1`.

---

## Project Structure

```text
go-gdrive-migration/
├── main.go
├── go.mod
├── config.example.yaml
├── build.ps1
├── run.bat
├── estimate.bat
├── dry-run.bat
├── internal/
│   ├── config/
│   ├── auth/
│   ├── drive/
│   ├── manifest/
│   ├── progress/
│   └── pipeline/
└── README.md
```

## Docs

- `docs/README.en.md` - documentation index (English).
- `docs/README.ru.md` - documentation index (Russian).
- `docs/ABOUT.md` - short beginner-friendly overview.
- `docs/AGENT_HANDOFF.md` - technical details, invariants, and change points.
# go-gdrive-migration

Russian docs: [`docs/README.ru.md`](./docs/README.ru.md)

Utility for fast folder and file copy between two Google Drive folders that are
accessible by the same account. Uses **server-side copy** via Google Drive API,
so files do not pass through your machine.

## Features

- Single static binary, no runtime dependencies (Windows / Linux / macOS).
- Parallel tree traversal and parallel file copy (configurable).
- Optional pre-flight scan: collect stats first, or skip scan and copy directly.
- Real-time progress bars for files and bytes.
- Skip empty folders (including empty subtrees).
- Resume after interruption with `manifest.jsonl`.
- Retry with exponential backoff for rate-limit and 5xx errors.
- Dry-run mode (show plan only).
- Per-job run report with copied files/bytes and duration.
- Run reports are saved to `reports/run-report-YYYYMMDD-HHMMSS.txt`.
- YAML-based configuration.

---

## Quick Start

### 1) Install Go

Download from [go.dev/dl](https://go.dev/dl) and verify:

```powershell
go version
```

### 2) Create OAuth Client ID

One-time setup. One `credentials.json` can be reused across machines/jobs.

1. Open [Google Cloud Console](https://console.cloud.google.com).
2. Create/select a project.
3. **APIs & Services -> Library -> Google Drive API -> Enable**.
4. **APIs & Services -> OAuth consent screen**:
   - User Type: External (personal Gmail) or Internal (Workspace).
   - Fill required fields (app name/support email).
   - Add scope: `https://www.googleapis.com/auth/drive`.
   - Add your email in Test users.
5. **APIs & Services -> Credentials -> Create Credentials -> OAuth client ID**:
   - Application type: **Desktop app**.
   - Download JSON and rename it to `credentials.json`.

### 3) Build

```powershell
cd D:\Projects\go-gdrive-migration
go mod tidy
.\build.ps1 -Target win
```

Binary output: `.\dist\go-gdrive-migration.exe`.

### 4) Configure

```powershell
Copy-Item config.example.yaml config.yaml
notepad config.yaml
```

Fill:

- `source_folder_id` - source root folder (ID or URL).
- `target_folder_id` - destination root folder (ID or URL).
- `sub_folder` - one or multiple paths (` , ` / `;` / newline), **or**
- `sub_folder_id` - one or multiple folder IDs (` , ` / `;` / newline).
- `options.target_subfolder_postfix` - optional suffix for target subfolder name.

Folder ID is the segment after `/folders/` in a Drive URL.

### 5) Place files together and run

```text
go-gdrive-migration\
├── go-gdrive-migration.exe
├── credentials.json
└── config.yaml
```

```powershell
.\dist\go-gdrive-migration.exe --config config.yaml
```

First run opens a browser for OAuth login. `token.json` is then stored locally
and reused on next runs.

---

## CLI Flags

```text
--config <path>   path to config.yaml (default: ./config.yaml)
--sub-folder      override sub_folder from config (path or list)
--sub-folder-id   override sub_folder_id from config (ID or list)
--target-subfolder-postfix  override options.target_subfolder_postfix
--yes             skip copy confirmation prompts (CI-friendly)
--dry-run         no copy (with skip_scan=false shows scan+plan)
--estimate        quick folders/files/bytes estimate and exit
--no-resume       ignore existing manifest and start from scratch
--version         show version
```

Priority for source selection: `--sub-folder-id` > `--sub-folder` > `config.yaml`.
Priority for postfix: `--target-subfolder-postfix` > `options.target_subfolder_postfix`.

Examples:

```powershell
.\dist\go-gdrive-migration.exe --config config.yaml --sub-folder "Folder1,Folder2" --estimate
.\dist\go-gdrive-migration.exe --config config.yaml --sub-folder-id "1AAA...,1BBB..." --yes
.\dist\go-gdrive-migration.exe --config config.yaml --sub-folder "MyFolder" --target-subfolder-postfix " Promo Materials"
```

---

## How It Works

```text
[1/6] Auth         -> OAuth flow (first run only)
[2/6] Resolve src  -> resolve sub_folder path under source_folder_id,
                      or use sub_folder_id directly
[3/6] Scan         -> if options.skip_scan=false: parallel scan and stats
[4/6] Prepare tgt  -> create/find target root folder for this job
[5/6] Plan         -> create mirrored folder structure in target
[6/6] Copy         -> server-side copy with retries + manifest resume
```

### Pre-flight Scan Modes

`options.skip_scan`:

- `false` - standard mode: scan first, then copy.
- `true` - direct-copy mode: skip pre-flight scan and start copy immediately.

Notes:

- with `dry_run: true` and `skip_scan: true`, no scan and no copy (no changes).
- with `skip_scan: true`, `skip_empty_folders` is ignored.
- keep `verify_checksums: false` for faster scan and lower metadata payload.

### Estimate Mode

Use `--estimate` or `options.estimate_only: true` for quick sizing only:

- counts folders/files/bytes;
- does not create target folders;
- does not copy files;
- does not use manifest/resume.

### Run Reports

When copy runs, a report is written to:

- `reports/run-report-YYYYMMDD-HHMMSS.txt`

For multiple `sub_folder` / `sub_folder_id` inputs, report includes a block per job.

---

## Resume and `manifest.jsonl`

Each processed file appends one JSON line:

```json
{"src_id":"abc","dst_id":"xyz","path":"Assets/img.png","size":12345,"status":"done","ts":"2026-04-26T18:00:00Z"}
{"src_id":"def","path":"Assets/big.psd","status":"failed","error":"...","ts":"..."}
```

On rerun with resume enabled (default), successful `src_id` values are skipped.
Failed entries are retried. To restart from scratch, remove `manifest.jsonl` or
run with `--no-resume`.

---

## Google Drive API Limits

Typical defaults:

- ~1000 requests / 100 seconds / user
- ~10,000 requests / 100 seconds / project

Tool retries 429 and rate-limit 403 automatically with exponential backoff.
If throttling is frequent, reduce `copy_workers` (for example to 6-8).

---

## Security

- `credentials.json` and `token.json` are in `.gitignore`.
- Token is stored locally only.
- OAuth callback server is temporary and bound to `127.0.0.1`.

---

## Project Structure

```text
go-gdrive-migration/
├── main.go
├── go.mod
├── config.example.yaml
├── build.ps1
├── run.bat
├── estimate.bat
├── dry-run.bat
├── internal/
│   ├── config/
│   ├── auth/
│   ├── drive/
│   ├── manifest/
│   ├── progress/
│   └── pipeline/
└── README.md
```

## Docs

- `docs/README.en.md` - documentation index (English).
- `docs/README.ru.md` - documentation index (Russian).
- `docs/ABOUT.md` - short beginner-friendly overview.
- `docs/AGENT_HANDOFF.md` - technical details, invariants, change points.
