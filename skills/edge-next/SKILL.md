---
name: edge-next
description: Get the user's next learning task from edge (one problem, picked for their weak areas). Use when the user types /edge-next or asks what to practice next.
allowed-tools: Bash(edge next), Bash(edge skip), Bash(edge skip --seen)
---
Run `edge next` and show its output to the user plainly: the problem, link, solution file path and today's progress. Add nothing else.

- Never mention the problem's topic, tags or likely technique. edge hides them on purpose.
- If the user says they've already solved it on LeetCode, run `edge skip --seen`. If they want a different problem for another reason, run `edge skip`, but tell them that skipping earns no credit.
- If it's a COLD RE-SOLVE or WARM-UP, remind them it's from scratch: no hints, and no peeking at their old file.
