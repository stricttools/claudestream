+++
title = "README.md"
+++
# claudestream

:-: var key="project.description"

## Install

```
uv pip install claudestream
```

## Quick start

### One-shot ask

```python
from claudestream import SessionConfig, SyncSession

config = SessionConfig(model="sonnet", profile="default")
with SyncSession(config) as session:
    result = session.ask("What is 2 + 2?")
    print(result.text)
```

### Streaming events

```python
from claudestream import SessionConfig, SyncSession, AssistantText, ToolUse, Result

config = SessionConfig(model="sonnet", profile="default")
with SyncSession(config) as session:
    for event in session.send("List the files in the current directory"):
        if isinstance(event, AssistantText):
            print(event.text, end="")
        elif isinstance(event, ToolUse):
            print(f"\n[tool: {event.name}]")
        elif isinstance(event, Result):
            print(f"\n(cost: ${event.total_cost_usd:.4f})")
```

### Async session

```python
import asyncio
from claudestream import SessionConfig, AsyncSession, AssistantText

async def main():
    config = SessionConfig(model="sonnet", profile="default")
    async with AsyncSession(config) as session:
        async for event in session.send("Hello!"):
            if isinstance(event, AssistantText):
                print(event.text, end="")

asyncio.run(main())
```

### Multi-turn conversation

```python
from claudestream import SessionConfig, SyncSession, AssistantText

config = SessionConfig(model="sonnet", profile="default")
with SyncSession(config) as session:
    for event in session.send("Remember that my name is Alice."):
        if isinstance(event, AssistantText):
            print(event.text, end="")
    print()
    for event in session.send("What is my name?"):
        if isinstance(event, AssistantText):
            print(event.text, end="")
```

## Custom tools

Define tools with the `@tool` decorator. claudestream auto-generates JSON Schema from type hints and serves them via MCP.

```python
from claudestream import tool, collect_tools, SessionConfig, SyncSession, AssistantText

@tool("my_server")
def lookup_weather(city: str, units: str = "celsius") -> str:
    """Look up current weather for a city.

    Args:
        city: City name to look up.
        units: Temperature units, celsius or fahrenheit.
    """
    return f"22 degrees {units} in {city}"

config = SessionConfig(
    model="sonnet",
    profile="default",
    tools=[lookup_weather._tool],
)
with SyncSession(config) as session:
    for event in session.send("What's the weather in Paris?"):
        if isinstance(event, AssistantText):
            print(event.text, end="")
```

## Agents

Agents are JSON-defined configurations with prompt templates, tool schemas, sandbox policies, and budget limits.

```python
from claudestream import (
    load_agent, invoke_agent_sync, SessionConfig, AssistantText,
)

agent = load_agent("code_reviewer")  # loads .claudestream/agents/code_reviewer.agent.json
config = SessionConfig(model="sonnet", profile="default")

with invoke_agent_sync(agent, config, variables={"file": "main.py"}) as session:
    for event in session.send("Review this file"):
        if isinstance(event, AssistantText):
            print(event.text, end="")
```

## Sandbox policies

Restrict which tools Claude can use and which paths it can write to.

```python
from claudestream import create_sandbox, SessionConfig, SyncSession

sandbox = create_sandbox(
    tools=["Read", "Bash"],
    write_paths=["/home/user/project"],
)
config = SessionConfig(model="sonnet", profile="default", sandbox=sandbox)

with SyncSession(config) as session:
    result = session.ask("Read the README and summarize it")
    print(result.text)
```

## CLI

:-: table-commands

## Configuration

:-: table-schema path="claudestream/_options.py" target="SessionConfig"

## Sandbox fields

:-: table-schema path="claudestream/policy.py" target="Sandbox"

## Dependencies

:-: table-dep path="pyproject.toml"

## Modules

:-: list-modules path="claudestream/"

## Project layout

:-: list-tree path="claudestream/" depth="1"

## License

MIT
