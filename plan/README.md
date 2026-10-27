# Загрузчик реплеев Dota 2 — план

В репозитории сохранены файлы запуска и этот план. Упоминания `cmd/spike`,
`scripts/generate_openapi.mjs`, `api/openapi.yaml`, тестов и `.scope-backup`
ниже и в разделах плана относятся к локальным материалам разработки:
в минимальную поставку они не включены. Инструкция запуска — в корневом
[README.md](../README.md); HTTP API описано в [05-api.md](05-api.md).

Объём (решение владельца, 2026-10-26): загрузчик реплеев, до ~100 в день.

1. `match_id` добавляются через HTTP API (`POST /api/v1/matches`).
2. Координаты реплея (`cluster`, `replay_salt`) — из OpenDota; если там их нет
   или OpenDota недоступна — из GC **одного своего аккаунта** в пределах его
   лимита (100 запросов / 24 ч).
3. Файл `.dem.bz2` скачивается с CDN Valve, проверяется и навсегда сохраняется в PostgreSQL.
4. Позже — веб-админка поверх того же API.

Без пула аккаунтов, без прокси, без автоматического поиска матчей. Прежний
план (пул, прокси, обнаружение через Web API/GC) сохранён в
`.scope-backup/20261026-loader/plan/` и не реализуется.

| Файл | Что внутри |
|------|-----------|
| [01-architecture.md](01-architecture.md) | компоненты, поток данных, GC-сессия, лимит, диагностика |
| [02-database.md](02-database.md) | таблицы и миграции |
| [03-tasks.md](03-tasks.md) | задачи и критерии готовности |
| [04-blockers.md](04-blockers.md) | решения владельца, открытые вопросы, риски |
| [05-api.md](05-api.md) | HTTP API для будущей админки |
| [06-deploy.md](06-deploy.md) | запуск, работа без остановки, корректная остановка |

Стек: Go, PostgreSQL + GORM, `settings.toml`, `net/http`;
`github.com/paralin/go-steam` + `github.com/paralin/go-dota2` — только для GC-запасного пути.

Быстрый старт:

```
collector init -config settings.toml      # ключ шифрования и API-токен
collector login -config settings.toml     # один раз: пароль + Steam Guard → токен (необязательно)
collector run -config settings.toml
curl -H "Authorization: Bearer <token>" -d '{"match_ids":[9063023867]}' http://127.0.0.1:8080/api/v1/matches
```

Без `collector login` работает только OpenDota.
Ручная проверка GC и CDN на живом Steam — `go run ./cmd/spike -match <id> -replay-file <new-file>`.
