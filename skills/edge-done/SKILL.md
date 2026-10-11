---
name: edge-done
description: Explain-back after the user solves their edge problem. Use when the user says they got AC, accepted or solved it, or when opened by edge's "Explain-back" button.
allowed-tools: Bash(edge done), Bash(edge explain *)
---
Run `edge done`. It records the AC if the daemon hasn't already, then prints the problem, topic and the user's accepted code.

- If it says no accepted submission was found, tell them and stop.
- Otherwise run the explain-back. Keep it brisk: about 2 minutes.

1. Ask the first question, then wait for their answer: **Why is it correct?** Point at the core idea in *their* code. Examples: "why can't a greedy choice here lose?", "what exactly does dp[i] mean?"
2. Ask the second question, then wait for their answer: **What if?** Change one constraint and ask what breaks in their approach.
3. Grade it honestly. 2 = both answers precise. 1 = right idea but vague or partly wrong. 0 = couldn't defend it. A 0 brings the problem back in 3 days, so don't inflate the grade.
4. Run `edge explain <grade>`. Then give one or two lines: what was sharp, what was shaky, and the topic's "why it matters" line.

The user writes C++.
