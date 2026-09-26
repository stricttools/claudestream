+++
title = "claudestream.events"
description = "Typed event dataclasses for every Claude Code stream output event, including assistant messages, tool use, permissions, and results."
nav_group = "API Reference"
nav_order = 11
+++

# claudestream.events

:-: table-events

:-: ref path="claudestream.events"

## Type Aliases

**ContentBlock** is a union of the 4 content block types (`TextBlock | ToolUseBlock | ThinkingBlock | ToolResultBlock`) and serves as the element type of `AssistantMessage.content`. Each block carries typed data from one segment of an assistant response, and the union lets consumers use isinstance checks or pattern matching to dispatch on block kind without casting.
