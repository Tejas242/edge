---
name: edge-stuck
description: Give a hint for the user's current edge problem, at exactly the level edge allows. Use when the user types /edge-stuck or is stuck on their edge task.
allowed-tools: Bash(edge stuck), Bash(edge stuck --take *)
---
Run `edge stuck`. It only looks: it prints the problem, sometimes the statement, their code, and one of these:

**HINT LEVEL N available.** Run `edge stuck --take N` to claim it; that prints **HINT LEVEL N granted**. Claiming is idempotent: running it again never spends a second hint.

**HINTS: locked / none.** Relay the message. Then act as a rubber duck only: ask them to explain their current approach and where it breaks. You may ask clarifying questions about *their* reasoning. Give no direction, technique, observation or counterexample.

**HINT LEVEL N granted** (after `--take`). Give exactly one hint at that level, and nothing beyond it:
- **Level 1, nudge:** one observation or question that points the right way. Name no technique or data structure.
- **Level 2, key idea:** name the core insight or technique and why it applies here. Give no algorithm steps.
- **Level 3, outline:** the algorithm as short steps plus the target complexity. No code.

Before hinting:
- If you don't know what they've tried, ask in one line first, then fit the hint to their attempt.
- For Codeforces and AtCoder problems the statement isn't fetched. If you're not certain you know this exact problem from its ID and title, ask them to paste the statement. Never hint from a guess.

Rules: the user writes C++. Never write solution code. Keep the hint short.
