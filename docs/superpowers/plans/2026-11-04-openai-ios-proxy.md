# OpenAI iOS Proxy Patch Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Выпустить `v0.6.1`, в котором восемь доменов OpenAI для iOS используют `217.60.179.6`, включая новый `chat.openai.com`.

**Architecture:** Изменяется только встроенный снимок `internal/smartdns/hosts.txt`; существующий точечный SmartDNS продолжает выдавать локальные A-ответы. Тесты фиксируют единственный новый адрес для каждого из восьми имён и новое общее количество 1026. Релиз публикуется существующим tag-triggered GitHub Actions workflow.

**Tech Stack:** Go 1.26, `miekg/dns`, Git, GitHub CLI/Actions, Markdown.

**Spec:** `docs/superpowers/specs/2026-11-04-openai-ios-proxy-design.md`

## Global Constraints

- Меняются только `chatgpt.com`, `auth.openai.com`, `cdn.oaistatic.com`, `ios.chat.openai.com`, `api.openai.com`, `chat.openai.com`, `files.oaiusercontent.com` и `oaistatic.com`.
- У первых семи имён удаляются `5.175.161.3` и `5.175.161.40`; у всех восьми остаётся ровно один A-адрес `217.60.179.6`.
- Остальные 1018 встроенных имён и поведение SmartDNS не меняются; wildcard-правила не добавляются.
- В README не указывать новый IP; обновить только количество имён и убрать утверждение, что все записи используют исключительно два старых адреса.
- Новая patch-версия — `v0.6.1` с датой 2026-11-04.

## Review Focus

- Отсутствующий ранее `chat.openai.com` должен обслуживаться локально, а не уходить upstream: тест восьми A-ответов в Task 1.
- Старые два адреса не должны оставаться дополнительными A-записями: проверка ровно одного ответа в Task 1.
- Счётчик уникальных имён должен вырасти ровно на один: проверки 1026 в Task 1.
- Несвязанные домены должны сохранить прежние адреса: сравнение встроенного списка и тест `releases.openai.com` в Task 1.
- Публикация не считается завершённой, пока release workflow не собрал и не приложил все 11 бинарников и `SHA256SUMS`: проверка в Task 3.

---

### Task 1: Обновить встроенные hosts и тесты

**Files:**
- Modify: `internal/smartdns/hosts.txt`
- Modify: `internal/smartdns/smartdns_test.go:96-116`
- Modify: `internal/cli/hosts_test.go:80-97`

**Interfaces:**
- Consumes: существующие `smartdns.Load("builtin")`, `smartdns.New`, `query`.
- Produces: встроенная таблица из 1026 имён; один A-ответ `217.60.179.6` для каждого из восьми целевых имён.

- [ ] **Step 1: Написать падающие проверки**

В `TestBuiltin` изменить ожидаемый `table.Count()` на 1026 и перебрать восемь целых имён: для каждого `query(r, name+".", dns.TypeA)` должен вернуть ровно один `*dns.A` со строкой `217.60.179.6`. В `TestHostsExportAndConfiguredStatus` заменить ожидаемый текст на `1026 names`.

- [ ] **Step 2: Убедиться, что новые проверки падают на старом списке**

Run: `go test ./internal/smartdns ./internal/cli`
Expected: FAIL: семь имён имеют два старых адреса, `chat.openai.com` уходит upstream, счётчик равен 1025.

- [ ] **Step 3: Изменить только целые записи hosts**

Заменить пары старых строк семи существующих имён одной строкой `217.60.179.6 <name>` и вставить `217.60.179.6 chat.openai.com` в алфавитном порядке. Не менять остальные записи.

- [ ] **Step 4: Прогнать пакетные тесты**

Run: `go test ./internal/smartdns ./internal/cli`
Expected: PASS.

### Task 2: Обновить описание версии

**Files:**
- Modify: `CHANGELOG.md:6`
- Modify: `README.md:22-26`
- Modify: `README.en.md:23-27`

**Interfaces:**
- Consumes: новый встроенный список из Task 1.
- Produces: документацию и release notes для `v0.6.1`.

- [ ] **Step 1: Добавить запись `0.6.1`**

В начало CHANGELOG добавить раздел `## [0.6.1] — 2026-11-04` с кратким описанием переключения восьми доменов и добавления `chat.openai.com`.

- [ ] **Step 2: Обновить текущие README**

В обеих языковых версиях заменить 1025 на 1026. Общее утверждение о двух прокси заменить формулировкой, что адреса берутся из встроенного hosts, без указания `217.60.179.6`.

- [ ] **Step 3: Проверить полный проект**

Run: `go vet ./... && go test -race ./...`
Expected: PASS без ошибок.

### Task 3: Опубликовать релиз и анонс

**Files:**
- No source changes; создаётся коммит, тег `v0.6.1`, GitHub Release и сообщение в топик Telegram DoQ.

**Interfaces:**
- Consumes: проверенное дерево Tasks 1–2 и существующий `.github/workflows/release.yml`.
- Produces: опубликованный `v0.6.1` с бинарниками и ссылка для пользователей.

- [ ] **Step 1: Проверить итоговый diff**

Run: `git diff --check && git status --short && git diff`
Expected: изменены только запланированные hosts, тесты, README, CHANGELOG и документы design/plan; нет артефактов сборки.

- [ ] **Step 2: Закоммитить и отправить patch-релиз**

Создать коммит `fix(smartdns): update OpenAI iOS proxy endpoints`, тег `v0.6.1`, отправить `main` и тег в `origin`.

- [ ] **Step 3: Дождаться workflow и оформить GitHub Release**

Через `gh run watch` дождаться успешного release workflow. Проверить 12 assets (11 бинарников и `SHA256SUMS`), затем задать заголовок `v0.6.1 — OpenAI iOS proxy update` и краткие русские notes с командами обновления, ссылающимися на `v0.6.1`.

- [ ] **Step 4: Проверить опубликованный релиз**

Run: `gh release view v0.6.1 --json name,body,url,assets`
Expected: релиз опубликован, notes и все assets присутствуют.

- [ ] **Step 5: Отправить анонс в Telegram**

В топик `https://t.me/zapret2keenetic/55256` отправить короткий русский анонс в стиле предыдущих релизов: версия `v0.6.1`, переключение восьми доменов OpenAI для iPhone, добавление `chat.openai.com`, ссылка на GitHub Release и команда обновления. Если доступ к отправке из текущей среды отсутствует, не имитировать публикацию: вернуть готовый текст и явно указать ограничение.
