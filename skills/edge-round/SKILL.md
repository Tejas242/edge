---
name: edge-round
description: Debrief the user's last contest round (Codeforces, AtCoder or LeetCode) and keep one lesson for the next one. Use when the user wants a round or contest debrief, asks how their contest went, or when opened by edge's "Debrief" button.
allowed-tools: Bash(edge round), Bash(edge round lesson *), Bash(edge rounds)
---
Run `edge round`. It prints the round they last sat: each problem, when it was solved (minutes into the round), how many rejected submissions it took, and which problems are now their upsolves.

Keep the debrief to about 3 minutes, then wait after each question:

1. **Where did the time go?** Point at the biggest gap in the timeline: a long stretch before a solve, or several rejections on one problem. Ask what happened in that stretch. Typical answers: misread the statement, wrong first idea, bug hunt, an edge case, got stuck and didn't move on.
2. **The first problem they didn't solve.** Ask what they tried and where it stopped. Don't explain the solution: it's their upsolve, and edge gives hints on the ladder when they work on it.
3. **One lesson.** Together, turn the answers into one concrete habit for the next round, as one short line of plain words with no symbols. Good: "reread the constraints before coding", "move on after 20 minutes stuck", "test n=1 before submitting". Bad: "be more careful".

Then run `edge round lesson <the line>`. edge shows it on the card before their next round.

If the round says "didn't sit it", skip the timeline: ask what got in the way, and keep a lesson about showing up instead (for example "register the day before").

Never mention the topic or technique of an upsolve problem.
