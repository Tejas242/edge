# edge ↔ Ember contract v2 (fix/base)

Status (`emberStatus`, ember.go) gained optional fields: see the struct. Producer: edge (fix/core fills the
task/round/owed/explain/debrief/health fields; fix/runtime fills Working, Paused, AsOfMs, WorkSec, Judging).

Action keys (`edge action <key>`). Every key that names a task or round is checked against it; a stale key
answers with an error instead of acting on another task.

| key | meaning |
|---|---|
| start / start:<id> | begin, or reopen the problem and editor if already active (idempotent) |
| open / open:<id> | same as start for an active task (kept for old adapters) |
| next | assign now, even before study time |
| skip:<id> | drop without credit: new before 10 min of work, upsolves any time (becomes dropped) |
| giveup:<id> | give up and learn (the teach flow); allowed when GiveUpInSec == 0 |
| hint:<id> | only when HintsReady |
| upsolve / upsolve:<round> | that round's next owed problem; parks an unstarted current task instead of failing |
| explain:<id>, debrief:<round> | open Claude for them |
| snooze, rest, keep, register:<r>, openround:<r>, skipround:<r>, undo:skipround:<r> | as before, plus undo |

`edge action` prints one line on success and exits 0; on failure it prints a plain sentence to stderr and exits 1
("Finish 2277C first"). Right after an action edge pushes a fresh status to Ember itself.

Delivery: Ember's socket answers one JSON line to an `edge` message: {"ok":true} when the edge plugin got it,
{"ok":false} when no edge plugin is running. edge waits ≤ 300 ms; no answer = an old Ember, treat as delivered.
Undelivered cards are kept and resent when Ember is back (edge sees the next ok:true).

Cards: Body may contain "\n" (Ember keeps line breaks). Cards with buttons carry `ttl_s` = how long their action
stays valid (default 600); edge sends `{"type":"remove","id":…}` when the card's task ends.

The full key list is `ActionKeys` (actions.go); `TestEveryActionKeyIsHandled` checks edge answers every one.
Errors are one sentence on stderr ("Finish 2277C first."); a stale id says which task is current now.

Added by fix/core (fields only): `emberCard.ttl_s`; `Task.work_days` (work by edge day, state.json); `Round.plan_was`
(the plan before "Can't make it", for its undo); stats `current.due` / `current.lapsed`. Card ids stay the event slot
(offer, hint, giveup, cold, drift, fail, late, ac, lapsed, undo, round…), so `remove` takes the same ids.
`giveUpInSec` is -1 when there's no started task. `health` is a plain sentence; the raw error is in the journal.

## v2.1 (v2/base): the trials

- `phase` (trial.go): baseline until 2026-11-05, trial1 from 11-06, trial2 from 11-20. Status and stats carry it.
- Trial 1 lives in consistency.go (`State.Cons`, `consistencyStats`, `consistencyStatus`, `consistencyAction`).
- Trial 2 lives in readiness.go (`State.Ready`, `readinessStats`, `readinessStatus`, `readinessAction`).
- New status fields: phase, cueMs, repDone, chain, missed, skipTokens, deadlineMs, pattern, drill, readyPct/Lo/Hi.
- Unknown action keys fall through to consistencyAction, then readinessAction. Add new keys to ActionKeys.
- Ember v2.1: `Card.Chips` (≤ 6 small choice chips under the body; a press sends the chip's key) and bars
  `Item.Lo`/`Item.Hi` (an interval band drawn behind the bar, same 0..max scale).

### v2.1 additions by v2/readiness (Trial 2; fields only)

- Stats `readiness` (in shadow from now; Ember shows it from trial2): `model` (version), `interview_rating` (CF
  scale, default 1400), `lc_offset` (400: zerotrac − 400 = CF scale), `r`/`lo`/`hi` (readiness and its 80%
  interval, 0..1), `target` (0.75), `patterns[]` {id, name, weight, theta, sigma, p, r, ready = p·r, lo, hi (bound
  p·r), solved, last (date), half_life_days}, `pick` {task, pattern, p, why} while the current task is Trial 2's
  pick, `mistakes` {total, all, last_30d, top}, `drills[]` {week, task, pattern, rating, p, result: pending | ac |
  missed}, `brier`.
- Status from trial2: `pattern` is the pattern's display name ("Graphs BFS/DFS"); `drill`; `readyPct/Lo/Hi`.
  For a drill, `deadlineMs` is set here when v2/consistency left it 0.
- Task kind `drill` (cold, no hints, can't be skipped). Keys: `mistake:<id>:<kind>` (kind: misread | edge |
  overflow | approach | bug), `drill` (start the drill now; opens it like start), `drill:later`.
- Cards: the mistake card has the fail card's id (`fail`) and replaces it, with 5 chips (and the hint buttons when
  due); the drill card is `drill`, chips Start · In 30 min.
- Config: `[patterns]` <id> = weight (default 1), `[readiness] interview_rating`.
