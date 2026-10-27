# Архитектура

## Поток данных

```
POST /api/v1/matches {match_ids}
  → matches (source=manual) + задача match_details
match_details (одна горутина, ≤ 1 запрос/с — бесплатный тариф OpenDota 60/мин)
  1. GET https://api.opendota.com/api/matches/{id} → cluster, replay_salt, метаданные
  2. любая ошибка OpenDota (404, нет salt, 429, сеть) → GC своего аккаунта:
     RequestMatchDetails → cluster, replay_salt, replay_state, raw_details
  → matches обновлён + задача replay_download (или skipped, если реплей
    NOT_RECORDED/EXPIRED по данным GC)
replay_download (downloader.workers горутин)
  GET http://replay{cluster}.valve.net/570/{match_id}_{salt}.dem.bz2
  → проверка: zstd/bzip2 по сигнатуре, PBDEMS2, поток до конца, ≤ 256 МиБ
  → replays (bytea, sha256) и итог задачи — одной транзакцией
```

Хосты `replay{cluster}.valve.net` отдают и китайские кластеры (проверено на
кластере 413, 2026-10-27; OpenDota для них пишет `dota2.com.cn`).

## Итог попытки задачи

| Ситуация | Что делаем |
|----------|-----------|
| успех | `done` (или `skipped` с причиной) |
| ошибка данных/сети | `Retry`: бэкофф 1 мин·2ⁿ ≤ 1 ч, после `max_attempts` (5) — `failed` |
| GC временно недоступен: лимит, бэкофф сессии, владелец играет, матч ещё идёт | `Requeue` до нужного времени, попытка не тратится |
| нет GC-аккаунта и нет данных в OpenDota | `Retry` с обеими причинами в `last_error` |
| остановка процесса | `Requeue` без штрафа |

`failed` повторяется вручную: `POST /api/v1/tasks/{id}/retry`.

## GC-сессия одного аккаунта

- Вход один раз: `collector login` (пароль и код Steam Guard — из env
  `STEAM_USER`/`STEAM_PASS`/`STEAM_GUARD_CODE` или с терминала, пароль не
  отображается) → токен Steam, в БД только зашифрованный `token_enc`.
  Пароль не хранится. Неудачный вход не повторяется (одна попытка + одна с кодом).
- Сессия открывается по требованию (первая задача без координат в OpenDota)
  и закрывается через 2 мин простоя, чтобы не мешать игре владельца.
- После логона ждём `PlayingSessionState`: если аккаунт играет где-то ещё —
  сессию не открываем (иначе Steam выбьет владельца из игры), повтор через 15 мин.
- Токен отвергнут → `steam_accounts.status = auth_failed` / `banned` + ERROR
  «run collector login»; GC больше не пробуется, OpenDota работает.
  `throttled` и сетевые ошибки — бэкофф (1 мин → 1 ч), статус не меняется.

## Лимит GC

- `steam_accounts.gc_daily_limit` (по умолчанию `[gc] daily_limit = 100`),
  `gc_remaining`, `gc_reset_at`: окно 24 ч начинается с первого запроса после
  `gc_reset_at`.
- Списание — атомарный `UPDATE … WHERE gc_remaining > 0` в одной транзакции с
  записью в `gc_requests`; повторный `collector login` лимит не обновляет.
- Таймаут ответа GC (`gc.request_timeout`) считаем признаком исчерпанного
  лимита: `gc_remaining = 0` до сброса окна.

## Структура кода

| Пакет | Что |
|-------|-----|
| `cmd/collector` | `init`, `migrate`, `login`, `run`, `status`, `healthcheck` |
| `cmd/spike` | ручная проверка на живом Steam |
| `internal/app` | жизненный цикл `run`: API, БД, advisory lock, воркеры, остановка |
| `internal/api` | HTTP API (`05-api.md`) |
| `internal/worker` | `opendota.go`, `gc.go`, `replay.go`, `run.go` |
| `internal/steamx` | GC-сессия по токену |
| `internal/store` | модели, миграции, очередь `tasks`, аккаунт и лимит |
| `internal/config`, `internal/secret` | `settings.toml`, AES-256-GCM |

## Настройки

`settings.toml` (пример — `settings.example.toml`), неизвестный ключ — ошибка с его именем.
`[gc]` и `[downloader]` применяются сразу (`atomic.Pointer[Config]`),
`[database]`, `[api]`, `[app]` — перезапуском внутри процесса, `[security]`
через API не меняется.

## Диагностика

Каждая проблема Steam/GC — ERROR в логе с именем аккаунта и
`steam_accounts.last_error`/`last_error_at`; каждая неудачная попытка задачи —
`tasks.last_error`. Всё видно в `GET /api/v1/status` и `GET /api/v1/account`.

## Безопасность

Токен Steam — только зашифрованно (`[security] secret_key`), в логах и ответах
API его нет. `settings.toml`, `spike-token.txt` — в `.gitignore`.
