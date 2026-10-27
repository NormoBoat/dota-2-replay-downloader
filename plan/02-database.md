# База данных

PostgreSQL 16 через GORM (pgx). DSN — `[database] dsn`.
Данные не удаляются: нет `DELETE`, `TRUNCATE`, `ON DELETE CASCADE`, задач очистки.

## Миграции

`internal/store/migrations/NNNN_*.sql`, каждая один раз и в транзакции
(`schema_migrations`); выполняются `collector migrate` и при старте `run`.

| Файл | Что |
|------|-----|
| `0001_init.sql` | исходная схема (вместе с таблицами прежнего объёма) |
| `0002_single_account.sql` | `steam_accounts.password_enc` и `gc_requests.proxy_id` — NULL допустим (пароль не храним, прокси нет) |

Схема меняется только новой миграцией; модели в `internal/store/models.go`
соответствуют SQL.

## Таблицы

| Таблица | Используется | Назначение |
|---------|--------------|-----------|
| `matches` | да | `match_id`, источник, `cluster`, `replay_salt`, `replay_state`, метаданные, `raw_details` (protobuf ответа GC) |
| `replays` | да | файл `bytea` (`STORAGE EXTERNAL`), `compression`, `size_bytes`, `sha256`, `url` |
| `tasks` | да | очередь: `match_details`, `replay_download`; одна активная задача на (kind, target) |
| `steam_accounts` | да, одна активная строка | `username`, `token_enc`, `status`, лимит `gc_*`, `last_error` |
| `gc_requests` | да | журнал каждого запроса к GC: результат, `eresult`, задержка |
| `app_state` | нет | ключ-значение |
| `proxies`, `players`, `match_players` | нет | прежний объём; оставлены, данные не удаляем |

## Ключевые запросы

- Выборка задачи — `UPDATE tasks … WHERE id = (SELECT … FOR UPDATE SKIP LOCKED) RETURNING *`.
- Лимит — `store.ReserveGC`: сброс окна по `gc_reset_at`, затем
  `UPDATE steam_accounts SET gc_remaining = gc_remaining - 1 WHERE … gc_remaining > 0`
  и строка `gc_requests` в той же транзакции.
- Списки не читают `replays.data` (`ReplayMeta`) и `matches.raw_details` (`Omit`).
- Один процесс на БД — `pg_try_advisory_lock(57002)`; при старте и остановке
  зависшие `running` возвращаются в `queued`.
