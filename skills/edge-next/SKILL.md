---
name: edge-next
description: Start or switch the user's edge problem. Use when the user asks for a problem or the next one, says they've already solved the current one, or wants a different one.
allowed-tools: Bash(edge start), Bash(edge status), Bash(edge skip), Bash(edge skip --seen)
---
The edge daemon usually hands out problems itself, through desktop notifications. You're only needed when the user asks in chat.

- **Wants to start or continue:** run `edge start`. It opens the problem in the browser and nvim in alacritty. Tell them in one line which problem it is.
- **Already solved it before:** run `edge skip --seen`, then `edge start`.
- **Wants a different one for any other reason:** run `edge skip`, then `edge start`. Say that skipping earns no credit.

Never mention the problem's topic, tags or likely technique; edge hides them on purpose. If it's a re-solve, say: from scratch, no hints. If it's an upsolve, name the round it's from: hints unlock as they work, like a new problem. Skipping an upsolve drops it for good.
