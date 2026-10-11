---
name: edge-giveup
description: Give up on the current edge task and learn the full solution. Use when the user types /edge-giveup.
allowed-tools: Bash(edge giveup)
---
Run `edge giveup`.

- **If it refuses (too early):** relay the wait time and suggest /edge-stuck. Teach nothing.
- **If it succeeds:** teach the full solution, because the problem comes back cold in 3 days and they must rebuild it alone:
  1. The key insight, and how someone would *find* it: which observation unlocks it.
  2. Why it's correct.
  3. A clean C++ solution with brief comments.
  4. The general pattern, for recognising it next time.
- **For Codeforces:** if you're not certain of the exact problem, ask them to paste the statement first.
