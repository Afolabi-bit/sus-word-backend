# SusWord Online Multiplayer — Go Backend Implementation Plan

**Target System**: High-concurrency, real-time WebSocket backend for SusWord  
**Language/Runtime**: Go 1.22+  
**Architecture Pattern**: Actor-inspired Goroutine per Room + Hub Registry + Dual-Pump WebSocket Connections  
**Document Status**: Ready for Implementation  

---

## 1. Executive Summary & Core Invariants

The SusWord backend manages online real-time multiplayer sessions of the social deduction game *SusWord*. The core gameplay requires strict information asymmetry: civilians receive the secret word and category, while the imposter receives only the category and a masked word placeholder (`null` / `"???"`). 

### Non-Negotiable Invariants
1. **Zero Information Leakage**: `SecretWord` and `ImposterID` MUST NEVER appear in any broadcast payload or shared room snapshot (`ROOM_STATE`). Role assignments must only be dispatched through point-to-point private messages (`ROLE_ASSIGNED`).
2. **Server-Authoritative State & Timing**: The client never controls game progression, turn order, or timers. Timers are driven by server-authoritative UTC timestamps (`endsAt`) and server timer goroutines.
3. **Race-Free Concurrency**: Each `Room` executes its own event loop goroutine (`Room.run()`). All player actions, timer ticks, connections, and disconnections are dispatched as events into the room's inbound channel, eliminating mutex contention on room state.
4. **Resilient Reconnection**: Transient mobile drops within a 60-second grace window allow players to reconnect using `(roomCode, displayName)`, seamlessly regaining their role and current phase state.

---

## 2. System Architecture & Concurrency Model

```
                    ┌────────────────────────┐
                    │ HTTP / WS Listen (Chi) │
                    └───────────┬────────────┘
                                │
        ┌───────────────────────┴───────────────────────┐
        ▼                                               ▼
┌─────────────────────────┐               ┌──────────────────────────┐
│  REST API Endpoints     │               │ WebSocket Upgrade (/ws)  │
│  - POST /api/rooms      │               └─────────────┬────────────┘
│  - GET  /api/rooms/{id} │                             │
│  - GET  /api/health     │                             ▼
└───────────┬─────────────┘               ┌──────────────────────────┐
            │                             │   Player Connection      │
            ▼                             │   - readPump (WS -> Chan)│
┌─────────────────────────┐               │   - writePump(Chan -> WS)│
│       Hub Registry      │               └─────────────┬────────────┘
│  - map[code]*Room (RWMux│                             │
│  - Room TTL Reaper      │                             ▼
└───────────┬─────────────┘               ┌──────────────────────────┐
            │                             │      Room Event Loop     │
            └───────────creates/routes───▶│      (single goroutine)  │
                                          │ - Inbound actions chan   │
                                          │ - State machine logic    │
                                          │ - Fan-out to Player.send │
                                          └──────────────────────────┘
```

### Concurrency Guarantees
- **Hub**: Thread-safe registry guarded by `sync.RWMutex`. Handles room creation, room lookup, and periodic reaper ticks.
- **Room**: Exactly **one goroutine** executes `room.run()`. It consumes from an `inbound` channel of typed events:
  - `PlayerJoinedEvent`
  - `PlayerLeftEvent`
  - `ClientActionEvent`
  - `TimerExpiredEvent`
  - `GraceTimeoutEvent`
- **Player**:
  - `readPump`: Reads WS frames, enforces rate limit (20 msg/s) and max payload size (4 KB), parses JSON envelope, and pushes to `room.inbound`.
  - `writePump`: Listens on buffered channel `send chan []byte` (buffer size 64) and flushes frames to the network. Handles WS ping/pong heartbeats.

---

## 3. Directory Layout & Module Structure

```
sus-word-backend/
├── cmd/
│   └── server/
│       └── main.go                 # Entrypoint, graceful shutdown, dependency wiring
├── internal/
│   ├── config/
│   │   └── config.go               # Env-based config (PORT, ALLOWED_ORIGINS, MAX_ROOMS, etc.)
│   ├── domain/
│   │   ├── models.go               # Player, Room, EliminationRecord, WordEntry
│   │   ├── phase.go                # Phase enum and state definitions
│   │   └── messages.go             # Client/Server message types and envelope schemas
│   ├── game/
│   │   ├── engine.go               # Game state machine transitions, guards, win logic
│   │   ├── words.go                # Word catalog, category mapping, history-aware picker
│   │   └── words_data.go           # Static word database (ported from frontend words.ts)
│   ├── player/
│   │   └── player.go               # Player entity, readPump, writePump, token bucket limiter
│   ├── hub/
│   │   ├── hub.go                  # Global room registry, concurrency-safe lookups, reaper
│   │   ├── room.go                 # Room actor event loop, message dispatcher, timer triggers
│   │   └── room_events.go          # Internal room event definitions
│   ├── ws/
│   │   └── handler.go              # WS upgrade, handshake validation, player instantiation
│   ├── api/
│   │   ├── handler.go              # REST routes: /api/rooms, /api/rooms/{code}, /api/health
│   │   └── response.go             # Standardized JSON response helpers
│   └── middleware/
│       ├── cors.go                 # Configurable CORS middleware
│       ├── ratelimit.go            # Per-IP rate limiting for REST endpoints
│       └── logging.go              # Structured HTTP logging with log/slog
├── Makefile                        # Build, run, test, lint targets
├── go.mod
├── go.sum
└── plan.md                         # This implementation plan
```

---

## 4. Detailed Component Specifications

### 4.1 Domain Models & Sanitization DTOs

To guarantee zero leakage, we define explicit outgoing DTOs:

```go
// Outgoing public room state broadcasted to all players
type PublicRoomState struct {
    RoomCode       string                  `json:"roomCode"`
    Phase          Phase                   `json:"phase"`
    HostID         string                  `json:"hostId"`
    TimerDuration  int                     `json:"timerDuration"`
    Players        []PublicPlayer          `json:"players"`
    ActivePlayers  []string                `json:"activePlayers"`
    EliminationLog []EliminationRecord     `json:"eliminationLog"`
    LastEliminated *EliminationRecord      `json:"lastEliminated"`
    Winner         string                  `json:"winner"`
}

type PublicPlayer struct {
    ID          string `json:"id"`
    DisplayName string `json:"displayName"`
    IsHost      bool   `json:"isHost"`
    IsActive    bool   `json:"isActive"`
    IsReady     bool   `json:"isReady"`
}

// Point-to-point private message (never broadcast)
type RoleAssignedPayload struct {
    Role           string  `json:"role"`           // "civilian" | "imposter"
    SecretWord     *string `json:"secretWord"`     // null for imposter
    SecretCategory string  `json:"secretCategory"` // visible to both
}
```

### 4.2 State Machine Transitions & Win Logic

| Current Phase | Action / Event | Guard / Preconditions | Next Phase | Server Side Actions |
|---|---|---|---|---|
| `lobby` | `START_GAME` | Caller is host, 4–10 players connected | `revealing` | Pick word (avoid history), assign 1 random imposter, shuffle reveal order, send private `ROLE_ASSIGNED` to each player, broadcast `ROOM_STATE` and first `REVEAL_TURN` |
| `revealing` | `PLAYER_READY` | Caller is `RevealOrder[RevealIndex]` | `revealing` or `ready` | Increment `RevealIndex`. If more players remain, broadcast next `REVEAL_TURN`. If all done, transition to `ready` and broadcast `ROOM_STATE` |
| `ready` | `START_DISCUSSION` | Caller is host | `discussing` | Set `TimerEndsAt = time.Now().Add(TimerDuration)`. Start timer goroutine. Broadcast `DISCUSSION_STARTED` |
| `discussing` | `DISCUSSION_ENDED` / Timer Expired | Server timer fires OR Host ends early | `voting` | Cancel timer if active. Broadcast `DISCUSSION_ENDED` + `ROOM_STATE(voting)` |
| `voting` | `ELIMINATE_PLAYER` | Caller is host, target is in `ActivePlayerIDs` | `result` or `gameOver` | Remove target from `ActivePlayerIDs`. Append to `EliminationLog`. Check win conditions (below). Broadcast `PLAYER_ELIMINATED` and either `ROOM_STATE(result)` or `GAME_OVER` |
| `result` | `NEXT_ROUND` | Caller is host, game not over | `discussing` | Reset `TimerEndsAt = time.Now().Add(TimerDuration)`. Start discussion timer. Broadcast `DISCUSSION_STARTED` |
| `gameOver` | `PLAY_AGAIN` | Caller is host | `revealing` | Preserve player list, reset active players to all connected players, pick new word + imposter, start new reveal cycle |
| `gameOver` | `NEW_GAME` | Caller is host | `lobby` | Reset state to lobby, clear word history, broadcast `ROOM_STATE(lobby)` |

#### Win Condition Evaluation Rule
After eliminating a target:
1. **Civilians Win**: If `target.ID == ImposterID`.
2. **Imposter Wins**: If imposter is NOT eliminated and `len(ActivePlayerIDs) <= 2` (or imposter constitutes 50% or more of active pool).
3. **Continue**: Otherwise, transition to `result`. The host can then advance via `NEXT_ROUND` back to `discussing`.

### 4.3 Word Selection Engine (`internal/game/words.go`)
- Port all word categories and terms from the frontend `words.ts` into a structured Go catalog.
- Support room-level memory: `WordHistory map[string]bool` records recently played words.
- Selection algorithm:
  1. Filter catalog for words not in `WordHistory`.
  2. If filtered list is empty, clear `WordHistory` and re-filter.
  3. Pick randomly using `crypto/rand`.
  4. Track word in history; trim history if it exceeds `floor(catalogSize / 3)`.

### 4.4 Disconnect & Reconnect Lifecycle
1. **Player Drops WebSocket**:
   - `readPump` detects disconnect / EOF / timeout.
   - Pushes `PlayerLeftEvent` to `room.inbound`.
   - Room marks `player.conn = nil` and records `disconnectedAt = time.Now()`.
   - If player was the host: elect next connected player in join order as host and broadcast `PLAYER_LEFT` with `newHostId`.
   - If room has **0 connected players**: start 5-minute room destruction grace timer.
2. **Player Reconnects** (`wss://host/ws?room=CODE&name=Alice`):
   - Hub finds room.
   - Room inspects existing players: if matching `(displayName)` found and disconnected within 60s:
     - Attach new connection to existing `Player` entity.
     - Cancel any disconnect timeout.
     - Send current `ROOM_STATE` to reconnecting client.
     - If game is active (`revealing`, `discussing`, etc.), resend private `ROLE_ASSIGNED`.
     - Broadcast `PLAYER_RECONNECTED` to room.
   - If name is not found and phase is `lobby`: join as new player (if room not full).
   - If name is found and connection is already active: return `NAME_TAKEN` error.

---

## 5. Step-by-Step Implementation Roadmap

### Phase 1: Foundation & Project Scaffolding
- [x] Initialize `go.mod` (module `sus-word-backend`).
- [x] Configure dependencies:
  - `github.com/go-chi/chi/v5` (routing)
  - `github.com/gorilla/websocket` (real-time transport)
  - `github.com/google/uuid` (UUID generation)
  - `golang.org/x/time/rate` (rate limiting)
- [x] Implement `internal/config/config.go` with environment variable loading:
  - `PORT`, `ALLOWED_ORIGINS`, `MAX_ROOMS`, `ROOM_TTL_MINUTES`, `MAX_CONN_PER_IP`.
- [x] Set up `slog` structured logger configured for console / JSON output.
- [x] Create `Makefile` with targets: `run`, `build`, `test`, `lint`.

### Phase 2: Domain Layer & Word Engine
- [x] Define enums and models in `internal/domain/`:
  - `Phase`, `Player`, `Room`, `EliminationRecord`, `WordEntry`.
  - Message envelopes and types for Client and Server payloads.
- [x] Build `internal/game/words.go` & `words_data.go`:
  - Port complete word dataset with category tagging.
  - Implement crypto-random selection avoiding recent room history.
  - Unit tests for word selection, category grouping, and history eviction.

### Phase 3: Game State Machine (`internal/game/engine.go`)
- [x] Implement state machine logic functions:
  - `CanStartGame(room)`
  - `StartGame(room)` -> generates imposter, secret word, reveal sequence
  - `AdvanceReveal(room, playerID)`
  - `StartDiscussion(room, duration)`
  - `EndDiscussion(room)`
  - `EliminatePlayer(room, targetID)` -> computes elimination, checks win conditions
  - `ResetForPlayAgain(room)`
  - `ResetForNewGame(room)`
- [x] Write exhaustive unit tests verifying state transitions, guard validations, and win conditions.

### Phase 4: Connection Management & WebSocket Layer (`internal/player`)
- [x] Implement `Player` struct with `readPump` and `writePump`:
  - Read deadline, write deadline, Pong handler.
  - Frame size validation (reject payloads > 4 KB).
  - Rate limiting (token bucket: 20 msg/sec per socket).
  - Tracking error count (disconnect on 10 consecutive invalid actions).
- [x] Implement safe write loop with channel buffering (`send chan []byte`).

### Phase 5: Room Actor & Hub Registry (`internal/hub`)
- [x] Implement `Room` actor (`internal/hub/room.go`):
  - Inbound event loop channel (`inbound chan RoomEvent`).
  - Single goroutine `Room.run()`.
  - Timer scheduling with cancellation (`time.Timer` for discussion ends).
  - Serialization to `PublicRoomState` ensuring zero role leakage.
  - Broadcast dispatcher: fan-out messages to all active players.
  - Targeted messaging: direct delivery to specific player `send` channel.
  - Grace period timers for host transfer and room abandonment.
- [x] Implement `Hub` (`internal/hub/hub.go`):
  - Concurrency-safe room map with `sync.RWMutex`.
  - `CreateRoom()` generating 6-char codes (excluding `0, O, 1, I, L` using `crypto/rand`).
  - `GetRoom(code)`.
  - Background reaper goroutine for TTL enforcement (30 min empty, 2 hr max).

### Phase 6: HTTP REST & WebSocket Upgrader
- [x] Implement `internal/api/handler.go`:
  - `POST /api/rooms`: validates body, creates room via Hub, returns `roomCode`, `wsUrl`, `expiresAt`.
  - `GET /api/rooms/{code}`: returns room metadata (`phase`, `playerCount`, `maxPlayers`, `joinable`).
  - `GET /api/health`: returns server status and active room count.
- [x] Implement `internal/ws/handler.go`:
  - Gorilla WebSocket upgrader with origin checking against `ALLOWED_ORIGINS`.
  - URL query parameter parsing (`room` and `name`).
  - Connection delegation to Hub -> Room -> Player goroutines.
- [x] Implement CORS and rate-limiting middleware.

### Phase 7: Anti-Abuse, Security & Hardening
- [x] IP-based rate limiting on room creation (`10 rooms/hour/IP`).
- [x] IP-based rate limiting on room join check (`30 requests/hour/IP`).
- [x] Max connection limit enforcement (max 5 concurrent sockets per IP, max 10 per room).
- [x] Panic recovery middleware for HTTP and room goroutines.
- [x] Graceful shutdown handler capturing `SIGINT`/`SIGTERM` to close WS connections cleanly.

### Phase 8: Testing, Verification & Integration
- [x] Unit Tests: words selector, phase state machine, models, envelopes, room actor, hub registry.
- [x] Integration Tests: multi-client simulated 4-player game session verifying zero role leakage, state progression, and imposter reveal.
- [x] Security Verification: IP connection limit enforcement and buffer saturation protection.

---

## 6. Edge Cases & Resilience Strategy

| Edge Case | Failure Mode | Mitigation Strategy |
|---|---|---|
| **Host drops during Lobby** | Lobby orphaned | Next player in join order is elected host. `PLAYER_LEFT` sent with `newHostId`. |
| **Host drops during Voting** | Game stalled | New host elected; receives same state, can submit elimination. |
| **All players disconnect** | Zombie room in memory | Room starts 5-minute abandonment timer. If no reconnection occurs, room is purged from Hub. |
| **Player disconnects during reveal** | Reveal sequence blocked | If disconnected player's turn comes, timeout can auto-skip or reconnecting resumes their turn. |
| **Slow or stalled WS client** | Memory leak / blocked buffer | `send` channel buffer cap (64). If full, drop connection and treat as disconnected. |
| **Rapid button mash (DoS)** | State mutation race | Single room actor processes sequentially. Inbound token bucket throttles sender; repeated illegal actions trigger error threshold disconnect. |
| **Clock skew between devices** | Clients disagree on timer | Server sends absolute UTC `endsAt` string. Server runs authoritative timer goroutine; timer end is triggered strictly by server clock. |

---

## 7. Frontend Integration Readiness Matrix

| Frontend Need (Next.js) | Backend Endpoint / WS Event | Verified Schema Compatibility |
|---|---|---|
| Create Room on `/online` | `POST /api/rooms` | Returns `{ roomCode, wsUrl, expiresAt }` |
| Validate Room Code on Join | `GET /api/rooms/{code}` | Returns `{ roomCode, phase, playerCount, maxPlayers, joinable }` |
| WebSocket Connection | `GET /ws?room={code}&name={name}` | Upgrades with standard envelope |
| Lobby synchronization | `ROOM_STATE` & `PLAYER_JOINED`/`PLAYER_LEFT` | Matches Zustand store interface |
| Secret Word Display | `ROLE_ASSIGNED` (private) | Civilians receive `{ role: "civilian", secretWord, secretCategory }`, Imposter receives `{ role: "imposter", secretWord: null, secretCategory }` |
| Turn-based reveal screen | `REVEAL_TURN` & `PLAYER_READY` | Indexed sequence matching UI flow |
| Synchronized countdown | `DISCUSSION_STARTED` `{ endsAt, durationSeconds }` | Computable via `Date.parse(endsAt) - Date.now()` |
| Elimination submission | `ELIMINATE_PLAYER` `{ playerId }` | Host-only action |
| Game end announcement | `GAME_OVER` `{ winner, imposterName, secretWord, eliminationLog }` | Single source of truth for post-match screen |
