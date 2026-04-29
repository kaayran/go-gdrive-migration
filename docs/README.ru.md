# go-gdrive-migration

English docs: [`../README.md`](../README.md)

Утилита для быстрого копирования папок и файлов между двумя папками Google
Drive, доступными одному аккаунту. Использует **server-side copy** через Google
Drive API, поэтому файлы не проходят через локальную машину.

## Возможности

- Один статический бинарь, без runtime-зависимостей (Windows / Linux / macOS).
- Параллельный обход дерева и параллельное копирование (настраивается).
- Опциональный pre-flight scan: можно сначала собрать статистику или сразу копировать.
- Прогресс-бары в реальном времени по файлам и по байтам.
- Пропуск пустых папок (включая пустые поддеревья).
- Resume после прерывания через `manifest.jsonl`.
- Retry с exponential backoff для rate-limit и 5xx ошибок.
- Dry-run режим (только план, без копирования).
- Отчет по каждому job: сколько скопировано и сколько заняло времени.
- Файлы отчета сохраняются в `reports/run-report-YYYYMMDD-HHMMSS.txt`.
- Конфиг в YAML.

---

## Быстрый старт

### 1) Установить Go

Скачать с [go.dev/dl](https://go.dev/dl), затем проверить:

```powershell
go version
```

### 2) Создать OAuth Client ID

Это разовая настройка. Один `credentials.json` можно использовать на разных машинах и задачах.

1. Открой [Google Cloud Console](https://console.cloud.google.com).
2. Создай/выбери проект.
3. **APIs & Services -> Library -> Google Drive API -> Enable**.
4. **APIs & Services -> OAuth consent screen**:
   - User Type: External (личный Gmail) или Internal (Workspace).
   - Заполни обязательные поля (app name/support email).
   - Добавь scope: `https://www.googleapis.com/auth/drive`.
   - Добавь свой email в Test users.
5. **APIs & Services -> Credentials -> Create Credentials -> OAuth client ID**:
   - Application type: **Desktop app**.
   - Скачай JSON и переименуй файл в `credentials.json`.

### 3) Сборка

```powershell
cd D:\Projects\go-gdrive-migration
go mod tidy
.\build.ps1 -Target win
```

Бинарь: `.\dist\go-gdrive-migration.exe`.

### 4) Настройка

```powershell
Copy-Item config.example.yaml config.yaml
notepad config.yaml
```

Заполни:

- `source_folder_id` - корневая папка источника (ID или URL).
- `target_folder_id` - корневая папка назначения (ID или URL).
- `sub_folder` - один или несколько путей (` , ` / `;` / новая строка), **или**
- `sub_folder_id` - один или несколько ID папок (` , ` / `;` / новая строка).
- `options.target_subfolder_postfix` - опциональный постфикс для имени target sub-folder.

ID папки — это часть после `/folders/` в URL Google Drive.

### 5) Положить файлы рядом и запустить

```text
go-gdrive-migration\
├── go-gdrive-migration.exe
├── credentials.json
└── config.yaml
```

```powershell
.\dist\go-gdrive-migration.exe --config config.yaml
```

При первом запуске откроется браузер для OAuth-авторизации. Затем `token.json`
сохранится локально и будет использоваться в следующих запусках.

---

## CLI-флаги

```text
--config <path>   путь к config.yaml (по умолчанию: ./config.yaml)
--sub-folder      переопределить sub_folder из config (путь или список)
--sub-folder-id   переопределить sub_folder_id из config (ID или список)
--target-subfolder-postfix  переопределить options.target_subfolder_postfix
--yes             пропустить подтверждение копирования (удобно для CI)
--dry-run         без копирования (при skip_scan=false показывает scan+plan)
--estimate        быстрый подсчет folders/files/bytes и выход
--no-resume       игнорировать существующий manifest и начать с нуля
--version         показать версию
```

Приоритет источника: `--sub-folder-id` > `--sub-folder` > `config.yaml`.
Приоритет постфикса: `--target-subfolder-postfix` > `options.target_subfolder_postfix`.

Примеры:

```powershell
.\dist\go-gdrive-migration.exe --config config.yaml --sub-folder "Folder1,Folder2" --estimate
.\dist\go-gdrive-migration.exe --config config.yaml --sub-folder-id "1AAA...,1BBB..." --yes
.\dist\go-gdrive-migration.exe --config config.yaml --sub-folder "MyFolder" --target-subfolder-postfix " Promo Materials"
```

---

## Как это работает

```text
[1/6] Auth         -> OAuth flow (только на первом запуске)
[2/6] Resolve src  -> резолв пути sub_folder относительно source_folder_id
                      или прямое использование sub_folder_id
[3/6] Scan         -> при options.skip_scan=false: параллельный scan и статистика
[4/6] Prepare tgt  -> создать/найти target root для текущего job
[5/6] Plan         -> создать зеркальную структуру папок в target
[6/6] Copy         -> server-side copy с retry + manifest resume
```

### Режимы pre-flight scan

`options.skip_scan`:

- `false` - стандартный режим: сначала scan, потом copy.
- `true` - direct-copy режим: scan пропускается, копирование начинается сразу.

Примечания:

- при `dry_run: true` и `skip_scan: true` не выполняется ни scan, ни copy;
- при `skip_scan: true` параметр `skip_empty_folders` игнорируется;
- для более быстрого scan оставляй `verify_checksums: false`.

### Режим estimate

Используй `--estimate` или `options.estimate_only: true` для быстрой оценки:

- считает folders/files/bytes;
- не создает папки в target;
- не копирует файлы;
- не использует manifest/resume.

### Отчеты по запуску

Во время copy создается отчет:

- `reports/run-report-YYYYMMDD-HHMMSS.txt`

Если указано несколько `sub_folder` / `sub_folder_id`, отчет содержит отдельный блок по каждому job.

---

## Resume и `manifest.jsonl`

Для каждого обработанного файла добавляется JSON-строка:

```json
{"src_id":"abc","dst_id":"xyz","path":"Assets/img.png","size":12345,"status":"done","ts":"2026-04-26T18:00:00Z"}
{"src_id":"def","path":"Assets/big.psd","status":"failed","error":"...","ts":"..."}
```

При повторном запуске (resume включен по умолчанию) успешные `src_id` пропускаются.
`failed` записи пробуются снова. Чтобы начать полностью заново — удали
`manifest.jsonl` или запусти с `--no-resume`.

---

## Лимиты Google Drive API

Типичные дефолтные лимиты:

- ~1000 запросов / 100 секунд / пользователь
- ~10 000 запросов / 100 секунд / проект

Утилита автоматически ретраит 429 и rate-limit 403 с exponential backoff.
Если часто упирается в лимиты, снизь `copy_workers` (например до 6-8).

---

## Безопасность

- `credentials.json` и `token.json` добавлены в `.gitignore`.
- Токен хранится только локально.
- OAuth callback-сервер временный и слушает `127.0.0.1`.

---

## Структура проекта

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

## Документация

- `docs/README.en.md` - индекс документации (English).
- `docs/README.ru.md` - индекс документации (Russian).
- `docs/ABOUT.md` - краткий обзор проекта.
- `docs/AGENT_HANDOFF.md` - технические детали, инварианты и точки изменений.
# Документация go-gdrive-migration (RU)

Этот раздел содержит внутреннюю документацию по проекту: быстрый вход для
новичков и технические детали для разработки.

## Что читать в первую очередь

- [`ABOUT.md`](./ABOUT.md) — краткое объяснение, как работает утилита и из каких
  модулей состоит.
- [`AGENT_HANDOFF.md`](./AGENT_HANDOFF.md) — технические инварианты, детали
  пайплайна и рекомендации по изменениям.
- [`PROMPT_TEMPLATE.md`](./PROMPT_TEMPLATE.md) — шаблон промпта для задач,
  связанных с проектом.

## Быстрая навигация

- Общий запуск и пользовательские инструкции: [`../README.md`](../README.md).
- Конфиг: `config.example.yaml`.
- Точка входа: `main.go`.

## Языковые версии

- Русская версия: этот файл.
- English version: [`README.en.md`](./README.en.md).
