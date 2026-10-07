// Package claudestream drives Claude Code as a subprocess and reads its
// output as typed events.
//
// A session starts Claude Code through `claudewheel profile exec`, so the
// subprocess gets the named profile's environment, and talks to it over the
// stream-json protocol: one JSON object per line on stdin and stdout. Every
// stdout line becomes one or more [Event] values. Assistant and user messages
// are always flattened into one event per content block ([AssistantText],
// [Thinking], [ToolUse], [ToolResult]), and successful file-modifying tool
// calls also yield [FileWrite] or [FileEdit].
//
// Configuration is explicit: [Config] names the model, the profile, the
// absolute paths of the claude and claudewheel binaries, and which session to
// start, resume, or continue. [Config.Validate] refuses anything missing or
// contradictory rather than filling in a guess.
//
// A [Sandbox] answers permission requests automatically from a tool
// allow-list and a set of writable paths; [Budget] thresholds yield
// [BudgetThreshold] events when the session's cost, turn count, or token
// count crosses them. Neither stops the session.
package claudestream
