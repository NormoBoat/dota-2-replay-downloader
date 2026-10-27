# HTTP API для веб-админки

`net/http` стандартной библиотеки, префикс `/api/v1`, JSON. Контракт —
`api/openapi.yaml`, генерируется из реестра маршрутов: `node scripts/generate_openapi.mjs`;
тест сверяет маршруты со спецификацией.

## Общее

- `Authorization: Bearer <[api] token>` на всём, кроме `GET /health`; без токена — 401.
- CORS — только origin из `[api] cors_origins`.
- Ошибки: `{"error":{"code","message"}}`; строгий JSON (неизвестное поле — 400).
- Списки: `limit` (1..500, по умолчанию 50), курсор `after`, ответ `{"items","next"}`.
- БД недоступна — 503; при остановке изменяющие запросы — `503 shutting_down`.
- API не удаляет данные и не возвращает секреты (токен Steam, `api.token`, DSN).

## Эндпоинты

| Метод и путь | Что |
|--------------|-----|
| `GET /health` | `{"ok","db","state"}`, 503 пока не `running` |
| `GET /status` | очередь по kind/status, аккаунт и его лимит, `gc_online`, размер БД |
| `GET /account` | аккаунт (без секретов, `has_token`), `online`, 50 последних запросов GC |
| `PATCH /account` | `{"status":"active"|"disabled","gc_daily_limit":N}`; вход — `collector login` |
| `POST /matches` | `{"match_ids":[…]}` (≤ 500) → матчи + задачи `match_details`; повтор не дублирует |
| `GET /matches` | фильтры `has_details`, `has_replay`, `from`, `to`, `game_mode` |
| `GET /matches/{match_id}` | матч, метаданные реплея, задачи по нему |
| `GET /matches/{match_id}/replay` | файл реплея как есть (сжатый) |
| `GET /tasks`, `GET /tasks/summary`, `GET /tasks/{id}` | очередь; фильтры `kind`, `status`, `target_id`, `since` |
| `POST /tasks` | `{"kind":"match_details"|"replay_download","target_id":N}` |
| `PATCH /tasks/{id}` | `priority`, `run_after` у `queued` |
| `POST /tasks/{id}/retry`, `POST /tasks/{id}/cancel` | повтор `failed`/`skipped`/`cancelled`; отмена `queued` |
| `GET /gc-requests` | журнал GC; фильтры `account_id`, `kind`, `result`, `since` |
| `GET /stats/daily` | по дням: запросы GC, матчи, реплеи, байты |
| `GET /settings`, `PUT /settings` | настройки с замаскированными секретами; `{"restart":bool}` |
| `POST /restart` | перезапуск внутри процесса |

## Проверка

`go test ./internal/api` (с `COLLECTOR_INTEGRATION=1` — на встроенном PostgreSQL).
