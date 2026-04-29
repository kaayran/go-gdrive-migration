# PROMPT_TEMPLATE

Reusable prompt template for another agent working on `go-gdrive-migration`.
Copy the block below, fill the `TODO` sections, and send it to the agent.

---

You are working on `go-gdrive-migration` (Go).

Before starting, read:
- `docs/AGENT_HANDOFF.md` (main architecture + invariants doc)
- optionally: `README.md`, `config.example.yaml`

## Project context

- The utility copies a `sub_folder` subtree from source Google Drive to target Google Drive.
- Copy uses server-side Drive API (`files.copy`) without local download/upload.
- Priorities:
  - speed,
  - correct resume behavior,
  - skipping empty folders,
  - predictable target sub-folder behavior.

## Invariants that must not break

1. Resume is based on `manifest.jsonl` (`status=done` + `src_id`).
2. Target root folder for sub_folder is persisted/reused (`status=target_root`).
3. With `skip_empty_folders=true`, empty folders/subtrees are not created.
4. Pipeline remains idempotent on repeated runs.

## My task

TODO: describe what to add/change/fix.

## What I need from you

1. Confirm understanding briefly.
2. List files you plan to change and why.
3. Implement the code changes.
4. Run build/lint/runtime checks when possible.
5. Provide a final report:
   - what changed,
   - which edge cases were handled,
   - what was tested and how,
   - what risks/limitations remain.

## Quality requirements

- Keep changes minimal and focused (no unrelated refactor).
- Handle errors explicitly with clear messages.
- Keep backward compatibility of existing config.
- If adding a new flag/config field:
  - update `config.example.yaml`,
  - update `README.md`,
  - update `docs/AGENT_HANDOFF.md` when architecture/invariants are affected.

## If behavior is unclear

- Diagnose first and share hypotheses.
- If multiple options exist, propose 2-3 with trade-offs, then implement the recommended one.
- If key data is missing, ask focused questions.

## Final response format

- `Changes` section (by file).
- `Validation` section (commands/checks run).
- `Risks and next steps` section.

---

## Short version (quick start)

Read `docs/AGENT_HANDOFF.md` and implement: **TODO**.
Do not break resume/target_root/skip_empty invariants.
After changes, report what changed, how you validated it, and remaining risks.
