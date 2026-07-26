# BF4DB Discord Bot 🎮

Discord bot that searches the [BF4DB](https://bf4db.com) Battlefield 4 cheater
database from a slash command: ban status, cheat score and the accounts linked
to an IP address or to a Discord user.

## Commands

| Command | What it does |
| --- | --- |
| `/ping` | latency check |
| `/bf4db global-search:<player>` | search by **name**, **IP address** or **BF4DB player id** (auto-detected) |
| `/bf4db discord-user:@user` | BF4 accounts linked to that Discord account |

Both options can be used in the same call. Results come back as embeds, colour
coded by the worst status found, with links to BF4DB, BF4 Cheat Report and
Battlefield Agency.

Status labels follow BF4DB's `is_banned` codes:

| Code | Label |
| ---: | --- |
| `-1` | ⚪ não reportado |
| `0` | 🟡 em análise |
| `1` | 🔴 banido |
| `2` | 🟢 limpo |
| `3` | 🔵 staff BF4DB |
| `4` | 🟠 glitch |
| `5` | 🟠 exploit |

## Search by name ⚠️

BF4DB's API endpoint for names (`GET /api/player/{name}/search`) currently
answers **HTTP 500 for every name** — a server-side fault in BF4DB
(`Undefined property: stdClass::$data`), not a client problem: the same
endpoint answers 200 for an IP or an EA GUID, and the bot's own pre-v2 code
gets the same 500 today.

The bot resolves names through BF4DB's **website** search
(`GET /player/search?query=`) and then hydrates every hit through the working
`GET /api/player/{id}` endpoint, so results carry the real ban code, cheat
score and GUIDs. When BF4DB fixes the API, the bot goes back to using it
automatically.

## Running it 🚀

```sh
cp .env-example .env   # fill DISCORD_BOT_TOKEN and BF4DB_API
docker compose up -d --build
```

Or without Docker:

```sh
go build -o bf4db-bot . && ./bf4db-bot
```

Images are published to `ghcr.io/the-eduardo/bf4db-discord-bot` for amd64 and
arm64.

### Configuration

| Variable | Required | Default | Meaning |
| --- | --- | --- | --- |
| `DISCORD_BOT_TOKEN` | yes | – | bot token |
| `BF4DB_API` | yes | – | BF4DB Patreon API key |
| `DISCORD_GUILD_ID` | no | global | register commands in one guild (instant) |
| `LOG_LEVEL` | no | `info` | `debug`, `info`, `warn`, `error` |
| `BF4DB_NAME_LIMIT` | no | `15` | max name-search hits to look up |
| `BF4DB_BASE_URL` | no | BF4DB API | override the API root |
| `BF4DB_WEB_URL` | no | BF4DB site | override the site root |

Flags: `-guild <id>` (overrides `DISCORD_GUILD_ID`) and `-rmcmd` to delete the
registered commands on shutdown.

The bot starts with **no privileged intents** and refuses to boot when a
required token is missing.

## Development 🧪

```sh
go test ./...        # unit tests, no network
go vet ./...
```

Live checks against the real API (needs a key, skipped by default):

```sh
BF4DB_API=<token> go test -tags live ./internal/bot -run TestLive -v
```

Layout:

```
main.go                 flags, logging, signal handling
internal/config         environment parsing and validation
internal/bot            slash commands, handlers, embed rendering
internal/bf4db          BF4DB API client (shared with the CLI tool)
```

`internal/bf4db` is kept in sync with
[BF4DB-Search-Tool](https://github.com/the-eduardo/BF4DB-Search-Tool); it is
copied rather than imported because that repository is private and this one is
public.

## License 📄

MIT — see [LICENSE](LICENSE).
