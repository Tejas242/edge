---
name: edge-done
description: Verify the user's accepted submission for their edge task, then run the explain-back. Use when the user types /edge-done or says they got AC.
allowed-tools: Bash(edge done), Bash(edge explain *)
---
Run `edge done`.

- If it errors (no accepted submission found), tell them and stop.
- If it prints ACCEPTED, run the explain-back:

1. Ask the first question, then wait for their answer: **Why is it correct?** It should be about this problem's core idea. Examples: "why does greedy choice X never lose?", "what does dp[i] mean and why does the transition cover every case?"
2. Ask the second question, then wait for their answer: **What if?** Change one constraint or detail and ask what breaks or changes in their approach.
3. Grade it honestly. 2 = both answers solid and precise. 1 = right idea, but vague or partly wrong. 0 = couldn't defend it. Don't inflate the grade: this decides whether the problem comes back.
4. Run `edge explain <grade>`. Give one or two lines on what was weak, if anything, and show the topic/why line that edge printed.
5. Ask whether they want the next task (/edge-next).

The user writes C++.
