+++
description = "Drive Claude Code from Python: run it as a subprocess and read its output as typed events, with async and sync sessions, sandbox policies, and tools you define in Python"
+++

# claudestream

Drive Claude Code from Python: run it as a subprocess and read its output as typed events, with async and sync sessions, sandbox policies, and tools you define in Python

claudestream wraps the `claude` CLI's `--output-format stream-json` / `--input-format stream-json` mode, providing typed Python events, async and sync APIs, permission policies, and a standalone CLI.

## Installation

```
uv add claudestream
```

Requires Python 3.14+ and `claude` CLI installed and authenticated.

## Quick Start

### Async

```python
from claudestream import AsyncSession, AssistantText, Result

async with AsyncSession(model="sonnet") as session:
    async for event in session.send("explain this repo"):
        match event:
            case AssistantText(text=t):
                print(t, end="")
            case Result(total_cost_usd=cost):
                print(f"\nCost: ${cost:.4f}")
```

### Sync

```python
from claudestream import SyncSession, AssistantText

with SyncSession() as session:
    for event in session.send("what is 2+2?"):
        if isinstance(event, AssistantText):
            print(event.text, end="")
```

### One-shot

```python
from claudestream import print_prompt

answer = print_prompt("what is the capital of France?")
print(answer)
```

### Multi-turn

```python
with SyncSession() as session:
    for event in session.send("remember: my name is Alice"):
        pass
    for event in session.send("what is my name?"):
        if isinstance(event, AssistantText):
            print(event.text, end="")
```

## CLI

```
claudestream send "explain this file" --model sonnet
claudestream stream "write a poem" -m opus
claudestream events "debug this" --raw
claudestream repl -m sonnet
```

## Permission Policies

```python
from claudestream import AsyncSession, allow_all, allow_builtins, allow_list

# Allow everything
async with AsyncSession(policy=allow_all()) as s: ...

# Allow only built-in Claude Code tools
async with AsyncSession(policy=allow_builtins()) as s: ...

# Allow specific tools only
async with AsyncSession(policy=allow_list(["Read", "Bash"])) as s: ...

# Custom callback
from claudestream import callback, Allow, Deny
policy = callback(lambda name, inp: Allow() if name != "Bash" else Deny("no shell"))
async with AsyncSession(policy=policy) as s: ...
```

## Event Types

| Event | Description |
|---|---|
| `SystemInit` | Session metadata (model, tools, version) |
| `AssistantText` | Text from the model |
| `ToolUse` | Tool call (name, input) |
| `ToolResult` | Tool execution result |
| `Thinking` | Extended thinking content |
| `StreamDelta` | Partial streaming token |
| `Result` | Turn complete (cost, duration) |
| `ApiRetry` | API retry notification |
| `RateLimit` | Rate limit status change |
| `PermissionRequest` | Tool permission request (when policy doesn't auto-resolve) |
| `UnknownEvent` | Forward-compatible catch-all |
