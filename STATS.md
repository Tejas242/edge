# edge stats

One JSON document with everything a dashboard needs: today, the current
problem, streaks, a year of daily activity, ratings over time, topics,
difficulty, re-solves, the hint experiment and edge's own health. Build any
view on it: an Ember page, a Noctalia widget, a waybar module, a web page.

## Getting it

| How | When to use |
|---|---|
| `edge stats` | One-off reads, scripts. Pretty JSON; `--compact` for one line. ~10 ms. |
| `$XDG_RUNTIME_DIR/edge/stats.json` | Live widgets. The daemon rewrites it (atomically) only when something changes, so watch it with inotify or poll its mtime. |
| Ember socket | Ember receives `{"type":"stats","stats":{…}}` inside an `edge` message whenever stats change, and at least every 5 minutes. |

The topic and tags of the **current** problem are never included. They
stay hidden until it's done, the same as in every other part of edge. Skipped
and ignored problems never show theirs either.

## Versioning

`version` is `1`. Within a version, fields are only ever **added**, never
renamed, removed or changed in meaning. Ignore fields you don't know.
Lists are always `[]`, never `null`. Times are RFC 3339 with the local
offset. Dates (`"2026-10-15"`) are **edge days**, which end at `day_ends`
(04:00 by default), so work at 01:00 belongs to the evening before.

## Schema

```jsonc
{
  "version": 1,
  "generated": "2026-10-15T21:04:05+05:30",
  "accounts": { "cf": "screenager", "lc": "screenager" },

  "today": {
    "date": "2026-10-15",
    "got": 0, "need": 1, "done": false,      // tasks credited / needed today
    "work_sec": 1260,                         // focused work today, incl. the current task
    "study_time": true,                       // problems are offered (and the gate can close) now
    "study_from": "19:00", "day_ends": "04:00",
    "gate": "closed",                         // closed | open | off
    "gate_why": "",                           // when open: done | pass | outside_study_time | not_enforcing | edge_trouble; "disabled" when off
    "pass_used_this_week": false
  },

  "current": {                                // null when there's no task
    "id": "cf:2257C", "source": "cf", "kind": "new",   // kind: new | resolve | reentry
    "title": "Spying on the Beaver", "url": "https://…", "rating": 1400,
    "assigned": "…", "started": "…",          // started missing until begun
    "work_sec": 1260, "drift_sec": 40,        // focused work / time on distraction sites
    "hints": 0, "hints_allowed": 1,           // used / unlocked now
    "next_hint_sec": 840,                     // more work until the next unlocks (ladder)
    "give_up_in_sec": 2340,                   // more work until giving up is allowed
    "fails": 2, "explain": -1, "credit": 0,
    "resolve_stage": 1                        // re-solves only
  },

  "streak": { "current": 4, "best": 9 },      // pass days bridge a streak but don't count

  "week": {                                   // Monday..Sunday
    "start": "2026-10-12", "credits": 3, "target": 12,
    "days": [ /* 7 × day, see calendar */ ]
  },

  "calendar": [                               // the last 365 edge days, oldest first, today last
    { "date": "2026-10-15", "credits": 1, "need": 1, "met": true, "pass": false,
      "work_sec": 2700,
      "cf_solved": 2 }                        // first Codeforces accepts that day, through edge or not
  ],

  "totals": {
    "tasks": 40, "solved": 31, "solo": 19, "assisted": 12, "gave_up": 5,
    "skipped": 3, "ignored": 1, "known": 2,   // known: solved too fast to be new
    "work_sec": 140000, "drift_sec": 3000, "hints": 25, "fails": 60,
    "passes": 2, "days_met": 28
  },

  "sources": {
    "cf": {
      "rating": 1208, "band_lo": 1200, "band_hi": 1500, "offset": 0,   // problems come from the band
      "solved_all_time": 240,
      "rating_history": [ { "at": "…", "rating": 1208, "contest": "Codeforces Round 1111 (Div. 2)" } ],
      "synced": "…"
    },
    "lc": { /* same; no solved_all_time */ }
  },

  "topics": [                                 // per source and tag, from problems worked through
    { "tag": "dp", "source": "cf", "focus_weight": 3,   // 0 = not a focus topic
      "tasks": 6, "solo": 2, "assisted": 3, "gave_up": 1, "avg_work_sec": 2400 }
  ],                                          // every focus topic appears, even with 0 tasks

  "difficulty": [                             // new problems by 100-point rating bucket
    { "source": "cf", "lo": 1300, "tasks": 8, "solo": 5, "assisted": 2, "gave_up": 1 }
  ],

  "resolves": {
    "due": 1, "scheduled": 7, "graduated": 4,
    "first_attempts": 10, "first_retained": 7,    // retention: first cold re-solves passed
    "upcoming": [ { "id": "…", "title": "…", "url": "…", "rating": 1300,
                    "due": "…", "stage": 1, "try": 0 } ]   // soonest first, up to 20
  },

  "experiment": {                             // which hint style makes solutions stick
    "status": "running",                      // running | decided
    "target_per_arm": 30,
    "arms": { "ladder":  { "new": 12, "solo": 7, "first_resolves": 9, "retained": 6 },
              "instant": { "new": 11, "solo": 5, "first_resolves": 8, "retained": 4 } },
    "verdict": "ladder", "p_ladder_better": 0.97    // only once decided; verdict: ladder | instant | none
  },

  "recent": [ /* up to 30 tasks, newest first, shaped like current, plus: */
    { "finished": "…", "outcome": "solo",     // solo | assisted | gaveup | skipped | ignored
      "known": false, "credit": 1,
      "focus": "dp", "tags": ["dp", "greedy"] }      // only for problems worked through
  ],

  "health": [ { "key": "cf", "since": "…", "msg": "can't reach Codeforces …" } ],
  "daemon": { "alive": true, "seen": "…", "gate_hook_seen": "…" }
}
```

## Ideas for views

- **Today ring:** `today.got / today.need`, colored by `today.gate`, with a timer
  from `current.work_sec`.
- **Year heatmap:** `calendar[].credits` (or `work_sec`), with `met` and `pass` as cell
  styles. `cf_solved` fills in the days before edge.
- **Rating line:** `sources.*.rating_history`, with the band `band_lo..band_hi` shaded.
- **Weak-spot radar:** `topics` where `focus_weight > 0`. Plot solo rate against
  tasks, so thin, hard topics stand out.
- **Re-solve queue:** `resolves.upcoming`. The retention gauge is
  `first_retained / first_attempts`.

A one-liner for a status bar:

```sh
jq -r '"\(.today.got)/\(.today.need) · 🔥\(.streak.current)"' "$XDG_RUNTIME_DIR/edge/stats.json"
```
