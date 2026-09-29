# Context management

`context.go` implements the agent's context window algorithm. It keeps the
message history sent to the model inside a token budget by pinning what must
never be lost and evicting whole conversation turns when the budget is
exceeded.

Phase 1 is purely local: no extra model calls, no summarization, no per-model
window. The budget is a fixed global for now.

## Data model

```go
type Context struct {
    Items []ContextItem
}

type ContextItem struct {
    Message     types.Message // the payload sent to the model
    ID          string        // stable identifier ("system-prompt", "item-N")
    Tokens      int           // estimated cost of Message.Content
    Importance  float32       // caller-supplied value; higher = keep longer
    Timestamp   time.Time
    Refetchable bool          // output can be regenerated (tool results)
    Pinned      bool          // never evicted
    Type        ItemType
}
```

`ItemType` classifies each item and feeds the scoring function:

| Type | Meaning | Weight | Pinned |
|------|---------|--------|--------|
| `System` | system prompt | 0 | yes |
| `Task` | the original user task | 0 | yes |
| `Memory` | reserved for summaries (phase 3) | 2 | yes |
| `Plan` | reserved for the agent's plan | 2 | yes |
| `RecentHistory` | normal user/assistant turns | 1 | no |
| `ToolResults` | tool observations | 0.5 | no |
| `Buffer` | reserved headroom (phase 2) | 0 | no |

## Algorithm

1. **Append.** Every message the agent sends or receives is appended to the
   `Context` via `AddMessage`. The first user message is auto-classified as
   `Task` and pinned; the system prompt is pinned by `NewContext`; later user
   messages become evictable `RecentHistory`.
2. **Group into turns.** `groupTurns` splits the ordered items into atomic
   units. An assistant message that carries `ToolCalls` absorbs the following
   `tool` result messages into the same turn. This is the correctness rule:
   tool calls and their results are never separated, otherwise the provider
   rejects the request.
3. **Budget check.** `reduce` returns immediately unless the summed tokens
   exceed `budget`.
4. **Protect.** The last `minRecentTurns` (4) turns are always kept, as are any
   turns containing a pinned item (`Pinned`, `System`, `Task`, `Memory`,
   `Plan`).
5. **Score candidates.** Remaining turns are scored by `turnScore`:
   `max(recency, typeWeight + Importance − refetchPenalty)` over their items.
   Recency is `(rank+1)/totalTurns`, so newer turns score higher; type weight
   makes `ToolResults` the cheapest to lose. The turn score is the maximum of
   its items so a single important item keeps the whole turn.
6. **Evict.** Candidates are sorted ascending by score and dropped turn by
   turn until the total is at or below budget. Dropping marks every item in
   the turn, so no orphaned tool result remains. Order of survivors is
   preserved.
7. **Emit.** `Build` returns the surviving `types.Message` values in order.

`Build` mutates the receiver: reduction is permanent, so once evicted an item
does not come back during this run.

## Function reference

- `NewContext(sysTemplate) Context` — creates a context seeded with the pinned
  system prompt.
- `estimateTokens(s string) int` — heuristic token count, `max(words,
  runes/4)`. Cheap and dependency-free; replaced by real usage in a later
  phase.
- `(*Context) AddMessage(msg)` — appends a message, inferring its `Type` and
  `Pinned` from role. First user message becomes `Task`.
- `(*Context) addTask(task)` — explicitly pins a task item with ID `"task"`.
- `(*Context) hasType(t)` — reports whether any item already has type `t`
  (used to make the first user message the task).
- `(*Context) totalTokens()` — sum of `Tokens` over all items.
- `(*Context) Build(budget int) []types.Message` — reduces to fit and returns
  the messages to send, in order.
- `groupTurns(items)` — partitions items into atomic turns, binding assistant
  tool calls to their results.
- `turnPinned(items, turn)` — true if a turn must never be evicted.
- `typeWeight(t)` — static per-`ItemType` score contribution.
- `turnScore(items, turn, rank, total)` — retention score for a turn.
- `(*Context) reduce(budget)` — the eviction loop described above.

## Integration

`Session` owns a `Context` (field `context`). `Session.Append` writes both the
durable `Messages` log and the `Context`. `Session.ContextMessages(budget)`
locks and calls `Build`. `Agent.getMessages` returns
`a.Session.ContextMessages(contextBudget)`, so every model call sees a reduced
history, while `Session.History()` still exposes the full log (used for the
trajectory artifact in `cmd/stubbs/main.go`).

## Budget

`MODEL_LIMIT = 1000000` and `contextBudget = MODEL_LIMIT * 1 / 2` (500k
estimated tokens). Phase 2 replaces these globals with a per-model context
window.

## Limitations / next phases

- No summarization yet: evicted turns are gone, not compressed.
- `Importance` and `Refetchable` are honored by the scorer but not populated
  by callers yet.
- No refetch of dropped tool results.
- Single global budget, not per-model.
- `estimateTokens` is approximate.
