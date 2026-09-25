"""Async session manager for the Claude Code stream-json protocol, handling process lifecycle, event parsing, and permission callbacks."""

from __future__ import annotations

import asyncio
import inspect
import logging
import random
import time

import msgspec
from collections.abc import AsyncIterator
from typing import Any, Callable

from claudestream.events import (
    ApiRetry,
    AskResult,
    AssistantMessage,
    AssistantText,
    ContextCategory,
    ContextUsage,
    ControlResponse,
    Event,
    FileEdit,
    FileWrite,
    McpRequest,
    PermissionRequest,
    RateLimit,
    Result,
    SystemInit,
    TextBlock,
    Thinking,
    ToolResult,
    ToolUse,
    UnknownEvent,
)
from claudestream.messages import AllowPermission, ControlRequest, DenyPermission, DialogCancelled, DialogCompleted, InitializeRequest, McpResponse, McpSetServers, UserMessage
from claudestream._options import SessionConfig, validate_budget
from claudestream.policy import Allow, Deny, Sandbox, sandbox_decide
from claudestream._process import ProcessConfig, ProcessManager, find_binary, check_version
from claudestream._protocol import flatten_event, parse_event, write_message
from claudestream._tools import Tool

log = logging.getLogger("claudestream")

_RECOVERY_MESSAGES = [
    "continue",
    "go ahead",
    "carry on",
    "proceed",
    "keep going",
    "resume where you left off",
]


class ClaudeStreamError(Exception):
    """Raised when the Claude Code subprocess fails."""

    def __init__(self, message: str, exit_code: int | None = None):
        super().__init__(message)
        self.exit_code = exit_code


class AsyncSession:
    """Async session managing a Claude Code subprocess.

    Usage::

        async with AsyncSession(model="sonnet") as session:
            async for event in session.send("hello"):
                print(event)
    """

    def __init__(self, config: SessionConfig):
        self._config = config
        self._binary = find_binary(config.binary)
        self._sandbox = config.sandbox
        self._callbacks: dict[type, list[Callable]] = {}
        self._on_turn_complete: list[Callable] = []
        self._on_error: list[Callable] = []
        self._on_close: list[Callable] = []
        self._active_turn = False
        self._last_result: Result | None = None
        self._cancelled = False
        self._turn_count: int = 0
        self._total_tokens: int = 0
        self._total_cost_usd: float = 0.0
        self._fired_thresholds: set[tuple[str, float]] = set()
        self._files_modified: set[str] = set()
        self._got_first_assistant: bool = False

        # User-defined tools, grouped by server name for MCP handling
        self._user_tools: list[Tool] = list(config.tools or [])
        self._tools_by_server: dict[str, list[Tool]] = {}
        for t in self._user_tools:
            self._tools_by_server.setdefault(t.server, []).append(t)

        # Transparent retry state (must be initialized before _build_process_config)
        self._resume_override: str | None = None
        self._restart_count: int = 0

        self._process_mgr = ProcessManager(self._build_process_config())

        # Events captured during startup handshake (before first send())
        self._startup_events: list[Event] = []

        # Control-request correlation: pending futures keyed by request_id, plus a
        # monotonic counter for generating collision-free ids ("ctrl_<n>").
        self._pending_controls: dict[str, asyncio.Future] = {}
        self._control_counter: int = 0
        # INVARIANT: exactly one stdout reader at any time. Both the turn loop and
        # a between-turns control read acquire this lock before reading stdout.
        self._stdout_lock: asyncio.Lock = asyncio.Lock()

        # Health timeout for _read_turn (default 30s, overridden by ProcessLimits)
        self._health_timeout: float = 30.0
        if config.process_limits is not None:
            self._health_timeout = config.process_limits.health_timeout

        # Stuck timeout: total silence before declaring subprocess stuck (default 120s)
        self._stuck_timeout: float = 120.0
        if config.process_limits is not None:
            self._stuck_timeout = config.process_limits.stuck_timeout

        # Session metadata (populated from SystemInit)
        self._session_id: str | None = None
        self._model_name: str | None = None
        self._tools: list[str] = []
        self._claude_version: str | None = None
        self._cwd: str = ""
        self._mcp_servers: list[str] = []
        self._permission_mode: str = ""

        # Maps a flattened ToolUse's tool_use_id to its tool name, so a later
        # ToolResult can be enriched with tool_name for renderers. flatten_event
        # stays pure; this correlation lives at the session level.
        self._tool_names: dict[str, str] = {}

    def _enrich_flattened(self, evt: Event) -> Event:
        """Correlate flattened tool events: record ToolUse names and stamp the
        matching name onto a ToolResult. Returns the (possibly replaced) event.
        """
        if isinstance(evt, ToolUse):
            if evt.tool_use_id:
                self._tool_names[evt.tool_use_id] = evt.name
        elif isinstance(evt, ToolResult) and evt.tool_name is None:
            name = self._tool_names.get(evt.tool_use_id)
            if name is not None:
                return msgspec.structs.replace(evt, tool_name=name)
        return evt

    def _build_process_config(self) -> ProcessConfig:
        """Build a ProcessConfig from the stored SessionConfig.

        This is the single mapping point between the user-facing config
        and the subprocess CLI flags.
        """
        config = self._config
        sandbox = config.sandbox

        # --- Sandbox → ProcessConfig fields (no flag round-trip) ---
        allowed_tools: list[str] = []
        bare = False
        dangerously_skip_permissions = False
        permission_prompt_tool: str | None = None

        if sandbox is not None:
            bare = sandbox.bare
            dangerously_skip_permissions = sandbox.skip_permissions
            if sandbox.tools is not None:
                allowed_tools = list(sandbox.tools)
            # Permission interception needed when we restrict tools or write paths
            if sandbox.tools is not None or sandbox.write_paths is not None:
                permission_prompt_tool = "stdio"

        # Add MCP wildcard patterns for each server with registered tools
        for server_name in self._tools_by_server:
            allowed_tools.append(f"mcp__{server_name}__*")

        # Enable stdio control protocol when SDK MCP tools are registered
        if self._tools_by_server:
            permission_prompt_tool = "stdio"

        # Explicit permission interception: route permission prompts (and
        # interactive tools like AskUserQuestion) to the consumer over the stdio
        # control channel, independent of any sandbox or SDK-tool config.
        if config.intercept_permissions:
            permission_prompt_tool = "stdio"

        # --- Option structs → ProcessConfig fields ---
        debug_enabled = False
        debug_filter: str | None = None
        debug_file: str | None = None
        if config.debug is not None:
            debug_enabled = config.debug.enabled
            debug_filter = config.debug.filter
            debug_file = config.debug.file

        mcp_config: list[str] = []
        strict_mcp_config = False
        if config.mcp is not None:
            mcp_config = list(config.mcp.config_files)
            strict_mcp_config = config.mcp.strict

        plugin_dirs: list[str] = []
        plugin_urls: list[str] = []
        if config.plugins is not None:
            plugin_dirs = list(config.plugins.dirs)
            plugin_urls = list(config.plugins.urls)

        verbose = True
        include_partial_messages = True
        include_hook_events = False
        replay_user_messages = False
        exclude_dynamic_prompt_sections = False
        if config.stream is not None:
            verbose = config.stream.verbose
            include_partial_messages = config.stream.include_partial_messages
            include_hook_events = config.stream.include_hook_events
            replay_user_messages = config.stream.replay_user_messages
            exclude_dynamic_prompt_sections = config.stream.exclude_dynamic_prompt_sections

        buffer_limit = 16_777_216
        shutdown_timeout = 5.0
        if config.process_limits is not None:
            buffer_limit = config.process_limits.buffer_limit
            shutdown_timeout = config.process_limits.shutdown_timeout

        # --- SessionResolution → ProcessConfig fields ---
        name: str | None = None
        session_id: str | None = None
        resume_session_id = self._resume_override or config.resume_session_id
        continue_session = False
        fork_session = False
        if config.session_resolution is not None:
            name = config.session_resolution.name
            session_id = config.session_resolution.session_id
            if config.session_resolution.resume_session_id is not None:
                resume_session_id = config.session_resolution.resume_session_id
            continue_session = config.session_resolution.continue_last
            fork_session = config.session_resolution.fork

        # --- json_schema dict → json_schema_str ---
        json_schema_str: str | None = None
        if config.json_schema is not None:
            import json
            json_schema_str = json.dumps(config.json_schema)

        # --- Profile → env vars ---
        from claudewheel.profile import resolve_profile
        merged_env: dict[str, str] = {}
        merged_env.update(resolve_profile(config.profile))
        merged_env.update(config.env or {})

        return ProcessConfig(
            binary=self._binary,
            cwd=config.cwd,
            model=config.model,
            system_prompt=config.system_prompt,
            permission_mode=config.permission_mode,
            allowed_tools=allowed_tools,
            permission_prompt_tool=permission_prompt_tool,
            resume_session_id=resume_session_id,
            extra_args=list(config.extra_args or []),
            env=merged_env or None,
            # String value flags
            effort=config.effort,
            json_schema_str=json_schema_str,
            fallback_model=config.fallback_model,
            name=name,
            setting_sources=config.setting_sources,
            settings=config.settings,
            debug_filter=debug_filter,
            debug_file=debug_file,
            agent=config.agent_name,
            agents_json=config.agents_json,
            from_pr=config.from_pr,
            session_id=session_id,
            # List flags
            betas=list(config.betas or []),
            add_dirs=list(config.add_dirs or []),
            builtin_tools=list(config.builtin_tools or []),
            file_specs=list(config.file_specs or []),
            mcp_config=mcp_config,
            plugin_dirs=plugin_dirs,
            plugin_urls=plugin_urls,
            # Bool flags
            bare=bare,
            brief=config.brief,
            continue_session=continue_session,
            fork_session=fork_session,
            no_session_persistence=config.no_persistence,
            strict_mcp_config=strict_mcp_config,
            include_hook_events=include_hook_events,
            replay_user_messages=replay_user_messages,
            exclude_dynamic_prompt_sections=exclude_dynamic_prompt_sections,
            debug=debug_enabled,
            verbose=verbose,
            include_partial_messages=include_partial_messages,
            dangerously_skip_permissions=dangerously_skip_permissions,
            # Process-level tuning
            buffer_limit=buffer_limit,
            shutdown_timeout=shutdown_timeout,
            # Hooks
            hooks=config.hooks or {},
        )

    # --- Properties ---

    @property
    def session_id(self) -> str | None:
        return self._session_id

    @property
    def model_name(self) -> str | None:
        return self._model_name

    @property
    def tools(self) -> list[str]:
        return self._tools

    @property
    def claude_version(self) -> str | None:
        return self._claude_version

    @property
    def last_result(self) -> Result | None:
        return self._last_result

    @property
    def turn_count(self) -> int:
        return self._turn_count

    @property
    def total_tokens(self) -> int:
        return self._total_tokens

    @property
    def total_cost_usd(self) -> float:
        return self._total_cost_usd

    @property
    def stderr_lines(self) -> list[str]:
        return self._process_mgr.stderr_lines

    @property
    def sandbox(self) -> Sandbox | None:
        return self._config.sandbox

    @property
    def user_tools(self) -> list[Tool]:
        return self._user_tools

    @property
    def is_alive(self) -> bool:
        return self._process_mgr.is_alive

    @property
    def active_turn(self) -> bool:
        return self._active_turn

    @property
    def cancelled(self) -> bool:
        return self._cancelled

    @property
    def files_modified(self) -> set[str]:
        """All files written or edited during this session (absolute paths, deduplicated).

        Note: Only tracks files modified via Write, Edit, and MultiEdit tools.
        Files modified via Bash tool calls are not tracked.
        """
        return set(self._files_modified)

    @property
    def process_pid(self) -> int | None:
        proc = self._process_mgr._process
        return proc.pid if proc else None

    @property
    def cwd(self) -> str:
        return self._cwd

    @property
    def mcp_servers(self) -> list[str]:
        return self._mcp_servers

    @property
    def permission_mode(self) -> str:
        return self._permission_mode

    @property
    def restart_count(self) -> int:
        return self._restart_count

    @property
    def config(self) -> SessionConfig:
        return self._config

    # --- Context manager ---

    async def __aenter__(self) -> AsyncSession:
        await self._start()
        return self

    async def __aexit__(self, *exc_info: Any) -> None:
        await self.close()

    async def _start(self) -> None:
        """Start the subprocess and complete the MCP handshake if tools are registered.

        When SDK MCP tools are registered, the full handshake is completed before
        returning so tools are ready by the time the first send() is called:
        1. Send InitializeRequest -> read ControlResponse
        2. Send McpSetServers -> read ControlResponse
        3. Read and respond to MCP handshake messages (initialize, notifications/initialized, tools/list)

        SystemInit (if received during handshake) is stored and drained on first send().
        """
        if self._config.budget is not None:
            validate_budget(self._config.budget)

        resume_session_id = self._resume_override or (
            self._config.session_resolution.resume_session_id
            if self._config.session_resolution is not None and self._config.session_resolution.resume_session_id is not None
            else self._config.resume_session_id
        )
        log.info("session_start_begin", extra={"is_resume": bool(resume_session_id), "resume_session_id": resume_session_id})

        version_check_timeout = 2.0
        if self._config.process_limits is not None:
            version_check_timeout = self._config.process_limits.version_check_timeout
        self._claude_version = await check_version(self._binary, timeout=version_check_timeout)

        await self._process_mgr.start()

        # Send InitializeRequest to register SDK MCP servers and/or hooks, or to
        # declare supported dialog kinds (which forces the handshake even with no
        # tools or hooks -- the CLI treats absence of the declaration as "cannot
        # display" and fails dialog-gated flows closed).
        # intercept_permissions also forces the handshake: can_use_tool control
        # requests are only delivered once initialize has been performed, so the
        # handshake must be sent even when there are no tools, hooks, or dialog
        # kinds.
        hooks = self._config.hooks or {}
        dialog_kinds = self._config.supported_dialog_kinds
        if self._user_tools or hooks or dialog_kinds is not None or self._config.intercept_permissions:
            server_names = list(self._tools_by_server.keys())
            init_req = InitializeRequest(
                sdk_mcp_servers=server_names,
                hooks=hooks,
                supported_dialog_kinds=dialog_kinds,
            )
            await write_message(self._process_mgr.stdin, init_req)

        if self._user_tools:
            # Complete the full MCP handshake so tools are ready before first send()
            log.info("completing MCP handshake for %d server(s)", len(self._tools_by_server))

            # Read the init ControlResponse
            await self._read_control_response(timeout=10.0)

            # Send McpSetServers for all SDK servers
            servers = {name: {"type": "sdk", "name": name} for name in self._tools_by_server}
            set_servers_req = McpSetServers(request_id="mcp_set_1", servers=servers)
            await write_message(self._process_mgr.stdin, set_servers_req)

            # Read the set_servers ControlResponse
            await self._read_control_response(timeout=10.0)

            # Complete the MCP handshake (initialize, notifications/initialized, tools/list)
            await self._run_mcp_handshake(timeout=10.0)

            log.info("MCP handshake complete, tools ready")
            log.info("session_start_complete", extra={"session_id": self._session_id, "tools_registered": len(self._tools)})
        else:
            log.info("process started, skipping SystemInit wait (captured on first send)")
            log.info("session_start_complete", extra={"session_id": self._session_id, "tools_registered": len(self._tools)})

    async def _read_control_response(self, timeout: float = 10.0) -> ControlResponse:
        """Read events from stdout until a ControlResponse is received.

        Any non-ControlResponse events encountered are stored in _startup_events.
        """
        import json as _json

        while True:
            line = await asyncio.wait_for(self._process_mgr.stdout.readline(), timeout=timeout)
            if not line:
                raise ClaudeStreamError("Subprocess closed stdout while waiting for ControlResponse")
            text = line.decode("utf-8", errors="replace").strip()
            if not text:
                continue
            try:
                raw = _json.loads(text)
            except _json.JSONDecodeError:
                log.warning("skipping non-JSON line during handshake: %s", text[:200])
                continue
            event = parse_event(raw)
            if isinstance(event, ControlResponse):
                log.info("received ControlResponse: request_id=%s", event.request_id)
                return event
            # Store non-ControlResponse events for later draining
            log.debug("storing startup event during handshake: %s", type(event).__name__)
            self._startup_events.append(event)

    async def _run_mcp_handshake(self, timeout: float = 10.0) -> None:
        """Complete the MCP protocol handshake for ALL registered MCP servers.

        Each server goes through: initialize -> notifications/initialized -> tools/list.
        We must wait for every server's tools/list before returning, otherwise
        send() will write a UserMessage to stdin before the CLI finishes
        handshaking remaining servers, causing a protocol-level hang.
        """
        import json as _json

        servers_pending = set(self._tools_by_server.keys())
        log.info("MCP handshake: waiting for %d server(s): %s", len(servers_pending), servers_pending)

        while servers_pending:
            line = await asyncio.wait_for(self._process_mgr.stdout.readline(), timeout=timeout)
            if not line:
                raise ClaudeStreamError("Subprocess closed stdout during MCP handshake")
            text = line.decode("utf-8", errors="replace").strip()
            if not text:
                continue
            try:
                raw = _json.loads(text)
            except _json.JSONDecodeError:
                log.warning("skipping non-JSON line during MCP handshake: %s", text[:200])
                continue
            event = parse_event(raw)

            if isinstance(event, McpRequest):
                method = event.message.get("method", "")
                server = event.server_name or ""
                log.info("MCP handshake: handling %s for server %s", method, server)
                await self._handle_mcp_request(event)
                if method == "tools/list" and server in servers_pending:
                    servers_pending.discard(server)
                    log.info("MCP handshake: server %s complete (%d remaining)", server, len(servers_pending))
            elif isinstance(event, ControlResponse):
                log.debug("storing ControlResponse during MCP handshake: request_id=%s", event.request_id)
                self._startup_events.append(event)
            else:
                log.debug("storing startup event during MCP handshake: %s", type(event).__name__)
                self._startup_events.append(event)

    async def close(self) -> None:
        """Shut down the session and kill the subprocess."""
        self._fail_pending_controls("session closed; control request lost")
        await self._fire_hooks(self._on_close, self)
        await self._process_mgr.close()

    # --- Cancel ---

    async def cancel(self, force: bool = False) -> None:
        """Cancel the current operation.

        Args:
            force: If False, close stdin (graceful). If True, terminate subprocess.
        """
        self._cancelled = True
        if force:
            await self._process_mgr.close()
        else:
            proc = self._process_mgr._process
            if proc and proc.stdin:
                proc.stdin.close()

    # --- Sending messages ---

    async def send(self, prompt: str | list, *, raw: bool = False) -> AsyncIterator[Event]:
        """Send a message and yield events until the turn completes.

        Args:
            prompt: The message to send. Can be a plain string or a list of
                content blocks (dicts) for multimodal input.
            raw: If True, yield raw protocol events (AssistantMessage,
                 ToolResultMessage). If False (default), yield flattened
                 convenience events (AssistantText, ToolUse, etc.).

        Yields:
            Event objects until a Result event is received.

        Raises:
            RuntimeError: If called while a previous turn is still active.
            ClaudeStreamError: If the subprocess dies unexpectedly.
        """
        self._cancelled = False

        if self._active_turn:
            raise RuntimeError(
                "Cannot send while a previous turn is active. "
                "Drain the event iterator or wait for the Result event."
            )

        self._active_turn = True
        self._last_result = None

        max_retries = 3

        try:
            msg = UserMessage(content=prompt, session_id=self._session_id or "")
            await write_message(self._process_mgr.stdin, msg)

            for attempt in range(max_retries + 1):
                try:
                    async for event in self._read_turn(raw=raw, _health_timeout=self._health_timeout):
                        yield event
                    return  # Normal completion
                except ClaudeStreamError as exc:
                    if "Subprocess stuck" not in str(exc) or attempt >= max_retries:
                        if attempt >= max_retries and "Subprocess stuck" in str(exc):
                            log.error("subprocess_retry_exhausted", extra={"attempts": max_retries, "session_id": self._session_id})
                        raise  # Not a liveness error, or max retries exceeded

                    log.warning("subprocess_retry_starting", extra={"attempt": attempt + 1, "max_retries": max_retries, "session_id": self._session_id})

                    # Restart the subprocess
                    await self._restart_subprocess()

                    # Send a recovery message instead of the original prompt
                    recovery = random.choice(_RECOVERY_MESSAGES)
                    recovery_msg = UserMessage(content=recovery, session_id=self._session_id or "")
                    await write_message(self._process_mgr.stdin, recovery_msg)

                    log.info("subprocess_retry_resumed", extra={"attempt": attempt + 1, "session_id": self._session_id, "recovery_message": recovery})

                    # Continue the loop -- _read_turn will be called again on the new subprocess
        except Exception as exc:
            await self._fire_hooks(self._on_error, self, exc)
            raise
        finally:
            self._active_turn = False

    async def ask(self, prompt: str | list) -> AskResult:
        """Send a prompt and return the complete response text with metadata."""
        parts: list[str] = []
        result_event: Result | None = None
        async for event in self.send(prompt):
            if isinstance(event, AssistantText):
                parts.append(event.text)
            elif isinstance(event, Result):
                result_event = event

        text = "".join(parts)
        if result_event:
            return AskResult(
                text=text,
                usage=result_event.usage,
                cost_usd=result_event.total_cost_usd,
                duration_ms=result_event.duration_ms,
                is_error=result_event.is_error,
                num_turns=result_event.num_turns,
                duration_api_ms=result_event.duration_api_ms,
                stop_reason=result_event.stop_reason,
                result=result_event.result,
                api_error_status=result_event.api_error_status,
                subtype=result_event.subtype,
            )
        return AskResult(text=text)

    async def _read_turn(
        self, *, raw: bool, _health_timeout: float = 30.0,
    ) -> AsyncIterator[Event]:
        """Read events for a single turn until Result is received."""
        if not self._process_mgr.is_alive:
            raise ClaudeStreamError(
                "Claude subprocess is not running",
                exit_code=None,
            )

        # Check if process already exited before we start reading
        if self._process_mgr._process is not None and self._process_mgr._process.returncode is not None:
            raise ClaudeStreamError(
                "Claude subprocess already exited",
                exit_code=self._process_mgr._process.returncode,
            )

        # Hold the stdout lock for the whole turn so a between-turns control read
        # can never race the turn loop for stdout lines.
        await self._stdout_lock.acquire()
        try:
            # Drain events captured during the startup MCP handshake
            if self._startup_events:
                startup_events = list(self._startup_events)
                self._startup_events.clear()
                for event in startup_events:
                    # Capture SystemInit metadata
                    if isinstance(event, SystemInit):
                        self._session_id = event.session_id
                        self._model_name = event.model
                        self._tools = list(event.tools)
                        self._cwd = event.cwd
                        self._mcp_servers = list(event.mcp_servers)
                        self._permission_mode = event.permission_mode
                        log.info(
                            "session started (from startup): id=%s model=%s tools=%d",
                            self._session_id,
                            self._model_name,
                            len(self._tools),
                        )

                    # Flatten or pass through
                    if raw:
                        events_to_yield = [event]
                    else:
                        events_to_yield = flatten_event(event, cwd=self._cwd or None)

                    for evt in events_to_yield:
                        evt = self._enrich_flattened(evt)
                        if isinstance(evt, (FileWrite, FileEdit)):
                            if evt.path:
                                self._files_modified.add(evt.path)
                        for cb in self._callbacks.get(type(evt), []):
                            cb(evt)
                        yield evt

            # Inline readline loop with event-based stuck detection (replaces read_events)
            import json as _json

            stream = self._process_mgr.stdout
            liveness_timeout = _health_timeout
            last_event_time = time.monotonic()

            while True:
                try:
                    line = await asyncio.wait_for(stream.readline(), timeout=liveness_timeout)
                except asyncio.TimeoutError:
                    elapsed = time.monotonic() - last_event_time
                    if elapsed >= self._stuck_timeout:
                        log.error("Subprocess stuck: no events for %.0fs (stuck_timeout=%.0fs)", elapsed, self._stuck_timeout)
                        await self._process_mgr.close()
                        raise ClaudeStreamError(f"Subprocess stuck: no events for {elapsed:.0f}s")
                    else:
                        log.debug("Readline timeout after %.0fs (stuck_timeout=%.0fs, still waiting)", elapsed, self._stuck_timeout)
                    continue

                if not line:
                    break
                text = line.decode("utf-8", errors="replace").strip()
                if not text:
                    continue
                try:
                    raw_data = _json.loads(text)
                except _json.JSONDecodeError:
                    log.warning("skipping non-JSON line: %s", text[:200])
                    continue
                event = parse_event(raw_data)
                last_event_time = time.monotonic()

                if self._cancelled:
                    raise ClaudeStreamError("Session cancelled")

                # Resolve a pending control request and swallow its response.
                # Unmatched ControlResponses fall through and are yielded as before.
                if isinstance(event, ControlResponse) and event.request_id in self._pending_controls:
                    self._resolve_control(event)
                    continue

                # Capture SystemInit (sent after first user message)
                if isinstance(event, SystemInit):
                    self._session_id = event.session_id
                    self._model_name = event.model
                    self._tools = list(event.tools)
                    self._cwd = event.cwd
                    self._mcp_servers = list(event.mcp_servers)
                    self._permission_mode = event.permission_mode
                    log.info(
                        "session started: id=%s model=%s tools=%d",
                        self._session_id,
                        self._model_name,
                        len(self._tools),
                    )

                # Handle permission requests via sandbox
                if isinstance(event, PermissionRequest):
                    await self._handle_permission(event)

                # Handle MCP requests for registered tools
                if isinstance(event, McpRequest):
                    handled = await self._handle_mcp_request(event)
                    if not handled:
                        # Unknown method or unknown server -- pass through
                        pass
                    else:
                        method = event.message.get("method", "")
                        if method in ("tools/list", "initialize", "notifications/initialized"):
                            # Handshake methods are fully internal, don't yield
                            continue
                    # tools/call is yielded so consumers can track calls

                # Per-type event logging (before flatten)
                if isinstance(event, Thinking):
                    log.info("event: Thinking (%d chars)", len(event.text))
                elif isinstance(event, Result):
                    log.info(
                        "event: Result (%.0fms, $%.4f, stop=%s)",
                        event.duration_ms,
                        event.total_cost_usd or 0,
                        event.stop_reason,
                    )
                elif isinstance(event, ApiRetry):
                    log.info(
                        "event: ApiRetry (attempt %d/%d, error=%s)",
                        event.attempt,
                        event.max_retries,
                        event.error,
                    )
                elif isinstance(event, RateLimit):
                    log.info(
                        "event: RateLimit (status=%s, resets_at=%s)",
                        event.status,
                        event.resets_at,
                    )
                elif isinstance(event, UnknownEvent):
                    if event.type == "system":
                        log.debug("event: Unknown system subtype (%s)", list(event.raw.keys()))
                    else:
                        log.warning("event: Unknown (%s)", list(event.raw.keys()))

                # Detect authentication errors (only on the first assistant
                # message AND only when the error field is set -- scanning
                # content text causes false positives when Claude talks
                # about authentication in its response).
                if isinstance(event, AssistantMessage) and not self._got_first_assistant:
                    self._got_first_assistant = True
                    error_lower = (event.error or "").lower()
                    if error_lower and any(
                        p in error_lower
                        for p in ("not logged in", "invalid authentication", "401")
                    ):
                        raise ClaudeStreamError(
                            "Authentication failed. Run `claude /login` to authenticate, "
                            "or check that your profile credentials are valid."
                        )

                # Flatten or pass through
                if raw:
                    events_to_yield = [event]
                else:
                    events_to_yield = flatten_event(event, cwd=self._cwd or None)

                for evt in events_to_yield:
                    evt = self._enrich_flattened(evt)
                    # Log flattened events
                    if isinstance(evt, ToolUse):
                        log.info("event: ToolUse (%s)", evt.name)
                    elif isinstance(evt, ToolResult):
                        log.info("event: ToolResult (%d chars)", len(str(evt.content)))

                    # Accumulate file-tracking events
                    if isinstance(evt, (FileWrite, FileEdit)):
                        if evt.path:
                            self._files_modified.add(evt.path)

                    # Fire callbacks before yielding
                    for cb in self._callbacks.get(type(evt), []):
                        cb(evt)
                    yield evt

                # Turn completes on Result
                if isinstance(event, Result):
                    self._last_result = event
                    self._turn_count += 1
                    if event.usage is not None:
                        self._total_tokens = event.usage.input_tokens + event.usage.output_tokens
                    self._total_cost_usd = event.total_cost_usd
                    for te in self._check_thresholds():
                        for cb in self._callbacks.get(type(te), []):
                            cb(te)
                        yield te
                    self._write_cost_log(event)
                    await self._fire_hooks(self._on_turn_complete, self, event)
                    return

            # stdout closed without a Result event
            rc = self._process_mgr._process.returncode if self._process_mgr._process else None
            raise ClaudeStreamError(
                "Claude subprocess closed stdout without sending a Result event",
                exit_code=rc,
            )
        finally:
            self._stdout_lock.release()

    async def _liveness_probe(self) -> None:
        """Check if the subprocess is still alive.

        Raises:
            ClaudeStreamError: If the process has died.
        """
        proc = self._process_mgr._process
        if proc is None or proc.returncode is not None:
            raise ClaudeStreamError("Subprocess died unexpectedly")

    async def _restart_subprocess(self) -> None:
        """Kill the stuck subprocess and restart with --resume to preserve session."""
        saved_session_id = self._session_id
        log.info("subprocess_restart_begin", extra={"session_id": saved_session_id})

        # Any control request issued to the dead process is unrecoverable.
        self._fail_pending_controls("subprocess restarted; control request lost")

        # Close the dead process
        await self._process_mgr.close()
        log.info("subprocess_restart_process_closed")

        # Set resume override so _build_process_config uses --resume
        self._resume_override = saved_session_id

        # Rebuild process manager with updated config
        self._process_mgr = ProcessManager(self._build_process_config())
        log.info("subprocess_restart_config_rebuilt", extra={"resume_session_id": saved_session_id})

        # Re-run startup (spawns subprocess, MCP handshake)
        await self._start()

        log.info("subprocess_restart_started", extra={"pid": self._process_mgr._process.pid if self._process_mgr._process else None})

        # Reset per-turn state; keep accumulated session state
        self._startup_events = []
        self._got_first_assistant = False
        self._active_turn = False
        self._cancelled = False

        self._restart_count += 1
        log.info("subprocess_restart_complete", extra={"restart_count": self._restart_count})

    async def _handle_permission(self, request: PermissionRequest) -> bool:
        """Apply sandbox rules to a permission request. Returns True if handled."""
        if self._sandbox is None:
            return False

        cwd = self._process_mgr.config.cwd or "."
        decision = sandbox_decide(self._sandbox, request.tool_name, request.tool_input, cwd)

        if isinstance(decision, Allow):
            updated = decision.updated_input if decision.updated_input else request.tool_input
            msg = AllowPermission(
                request_id=request.request_id,
                updated_input=updated,
            )
        else:
            msg = DenyPermission(
                request_id=request.request_id,
                message=decision.message,
            )

        await write_message(self._process_mgr.stdin, msg)
        return True

    async def _handle_mcp_request(self, request: McpRequest) -> bool:
        """Handle an MCP JSON-RPC request. Returns True if handled."""
        tools = self._tools_by_server.get(request.server_name)
        if tools is None:
            return False

        message = request.message
        method = message.get("method", "")
        rpc_id = message.get("id")

        if method == "tools/list":
            response = {
                "jsonrpc": "2.0",
                "id": rpc_id,
                "result": {
                    "tools": [
                        {
                            "name": t.name,
                            "description": t.description,
                            "inputSchema": t.input_schema,
                            "_meta": {"anthropic": {"alwaysLoad": True}},
                        }
                        for t in tools
                    ],
                },
            }
            msg = McpResponse(request_id=request.request_id, mcp_response=response)
            await write_message(self._process_mgr.stdin, msg)
            return True

        if method == "initialize":
            response = {
                "jsonrpc": "2.0",
                "id": rpc_id,
                "result": {
                    "protocolVersion": "2025-11-25",
                    "capabilities": {"tools": {}},
                    "serverInfo": {
                        "name": request.server_name,
                        "version": "1.0.0",
                    },
                },
            }
            msg = McpResponse(request_id=request.request_id, mcp_response=response)
            await write_message(self._process_mgr.stdin, msg)
            return True

        if method == "notifications/initialized":
            response = {
                "jsonrpc": "2.0",
                "result": {},
                "id": rpc_id or 0,
            }
            msg = McpResponse(request_id=request.request_id, mcp_response=response)
            await write_message(self._process_mgr.stdin, msg)
            return True

        if method == "tools/call":
            params = message.get("params", {})
            tool_name = params.get("name", "")
            arguments = params.get("arguments", {})

            # Find the tool by name
            handler = None
            matched_tool: Tool | None = None
            for t in tools:
                if t.name == tool_name:
                    handler = t.handler
                    matched_tool = t
                    break

            if handler is None:
                response = {
                    "jsonrpc": "2.0",
                    "id": rpc_id,
                    "error": {"code": -32601, "message": f"Tool '{tool_name}' not found"},
                }
            else:
                try:
                    # Inject tool_context for params listed in tool.inject
                    if matched_tool is not None and matched_tool.inject:
                        tool_context = self._config.tool_context
                        if tool_context is None:
                            raise RuntimeError(
                                f"Tool '{tool_name}' requires tool_context but none was provided in SessionConfig"
                            )
                        for inject_param in matched_tool.inject:
                            arguments[inject_param] = tool_context

                    if inspect.iscoroutinefunction(handler):
                        result = await handler(**arguments)
                    else:
                        result = handler(**arguments)
                    if isinstance(result, (dict, list)):
                        import json
                        result_text = json.dumps(result)
                    elif isinstance(result, str):
                        result_text = result
                    else:
                        result_text = str(result)
                    response = {
                        "jsonrpc": "2.0",
                        "id": rpc_id,
                        "result": {
                            "content": [{"type": "text", "text": result_text}],
                        },
                    }
                except Exception as e:
                    response = {
                        "jsonrpc": "2.0",
                        "id": rpc_id,
                        "error": {"code": -32000, "message": str(e)},
                    }

            msg = McpResponse(request_id=request.request_id, mcp_response=response)
            await write_message(self._process_mgr.stdin, msg)
            return True

        # Unknown JSON-RPC method
        return False

    # --- Control-request correlation ---

    def _resolve_control(self, event: ControlResponse) -> bool:
        """Resolve the pending future for a control response. Returns True if matched."""
        future = self._pending_controls.pop(event.request_id, None)
        if future is None:
            return False
        if not future.done():
            if event.subtype == "error":
                future.set_exception(
                    ClaudeStreamError(f"Control request failed: {event.error or 'unknown error'}")
                )
            else:
                future.set_result(event.response or {})
        return True

    def _fail_pending_controls(self, message: str) -> None:
        """Fail every pending control future with ClaudeStreamError and clear the registry."""
        for future in list(self._pending_controls.values()):
            if not future.done():
                future.set_exception(ClaudeStreamError(message))
        self._pending_controls.clear()

    async def _control_request(
        self, subtype: str, payload: dict | None = None, *, timeout: float = 30.0
    ) -> dict:
        """Issue a control request and await its correlated control_response.

        During an active turn the turn loop resolves the future (no stdout read
        happens here). Between turns this drives its own scoped stdout read loop,
        buffering unrelated events for the next turn.

        Returns the inner ``response`` dict on success. Raises ClaudeStreamError
        on process death, CLI error response, or timeout.
        """
        if not self._process_mgr.is_alive:
            raise ClaudeStreamError("Claude subprocess is not running")

        self._control_counter += 1
        request_id = f"ctrl_{self._control_counter}"
        loop = asyncio.get_running_loop()
        future: asyncio.Future = loop.create_future()
        self._pending_controls[request_id] = future

        req = ControlRequest(request_id=request_id, subtype=subtype, payload=payload or {})
        try:
            await write_message(self._process_mgr.stdin, req)
        except Exception:
            self._pending_controls.pop(request_id, None)
            raise

        if self._active_turn:
            # A turn loop is reading stdout and will resolve the future.
            try:
                return await asyncio.wait_for(future, timeout)
            except asyncio.TimeoutError:
                self._pending_controls.pop(request_id, None)
                raise ClaudeStreamError(
                    f"Control request '{subtype}' timed out after {timeout}s"
                )
        else:
            # No turn active: drive a scoped stdout read loop ourselves.
            try:
                return await self._read_control_result(request_id, future, timeout)
            finally:
                self._pending_controls.pop(request_id, None)

    async def _read_control_result(
        self, request_id: str, future: asyncio.Future, timeout: float
    ) -> dict:
        """Read stdout until the matching control_response resolves the future.

        Non-matching events are buffered into _startup_events for the next turn.
        Respects the per-read health timeout and the overall operation timeout;
        raises ClaudeStreamError on EOF.
        """
        import json as _json

        deadline = time.monotonic() + timeout
        async with self._stdout_lock:
            while not future.done():
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    raise ClaudeStreamError(
                        f"Control request timed out after {timeout}s"
                    )
                read_timeout = min(remaining, self._health_timeout)
                try:
                    line = await asyncio.wait_for(
                        self._process_mgr.stdout.readline(), timeout=read_timeout
                    )
                except asyncio.TimeoutError:
                    continue  # deadline check above enforces the overall timeout
                if not line:
                    raise ClaudeStreamError(
                        "Subprocess closed stdout while waiting for control response"
                    )
                text = line.decode("utf-8", errors="replace").strip()
                if not text:
                    continue
                try:
                    raw = _json.loads(text)
                except _json.JSONDecodeError:
                    log.warning("skipping non-JSON line during control read: %s", text[:200])
                    continue
                event = parse_event(raw)
                if isinstance(event, ControlResponse) and event.request_id in self._pending_controls:
                    self._resolve_control(event)
                else:
                    self._startup_events.append(event)
        return future.result()

    # --- Budget observation ---

    def _check_thresholds(self) -> list:
        """Check all threshold lists and return BudgetThreshold events for newly-crossed thresholds."""
        from .events import BudgetThreshold

        if self._config.budget is None:
            return []

        events = []
        budget = self._config.budget

        # Fixed metric order: cost, turns, tokens
        checks = [
            ("cost", budget.cost_thresholds, self._total_cost_usd),
            ("turns", budget.turn_thresholds, float(self._turn_count)),
            ("tokens", budget.token_thresholds, float(self._total_tokens)),
        ]

        for metric, thresholds, current in checks:
            for t in sorted(thresholds):
                key = (metric, float(t))
                if key not in self._fired_thresholds and current >= t:
                    self._fired_thresholds.add(key)
                    events.append(BudgetThreshold(
                        metric=metric,
                        threshold=float(t),
                        current_value=current,
                    ))

        return events

    def _write_cost_log(self, result) -> None:
        """Append a JSONL line to the cost log file if configured."""
        path = self._config.cost_log_path
        if path is None:
            return

        import json
        from datetime import datetime, timezone

        record = {
            "session_id": self._session_id,
            "model": self._model_name,
            "turn": self._turn_count,
            "total_cost_usd": self._total_cost_usd,
            "stop_reason": result.stop_reason,
            "duration_ms": result.duration_ms,
            "duration_api_ms": result.duration_api_ms,
            "timestamp": datetime.now(timezone.utc).isoformat(),
        }
        if result.usage is not None:
            record["input_tokens"] = result.usage.input_tokens
            record["output_tokens"] = result.usage.output_tokens
            record["cache_creation_input_tokens"] = result.usage.cache_creation_input_tokens
            record["cache_read_input_tokens"] = result.usage.cache_read_input_tokens

        with open(path, "a") as f:
            f.write(json.dumps(record) + "\n")

    # --- Callbacks ---

    def on(self, event_type: type[Event], handler: Callable[[Any], None]) -> None:
        """Register a callback for a specific event type.

        The callback fires during iteration, before the event is yielded.
        """
        if event_type not in self._callbacks:
            self._callbacks[event_type] = []
        self._callbacks[event_type].append(handler)

    # --- Lifecycle hooks ---

    def on_turn_complete(self, hook: Callable) -> None:
        """Register a hook that fires after each turn completes (after Result event).

        Hook signature: ``async def hook(session, result)`` or ``def hook(session, result)``.
        Hooks run in registration order. Errors are logged but do not propagate.
        """
        self._on_turn_complete.append(hook)

    def on_error(self, hook: Callable) -> None:
        """Register a hook that fires when a turn fails with an exception.

        Hook signature: ``async def hook(session, exception)`` or ``def hook(session, exception)``.
        Hooks run in registration order. Errors are logged but do not propagate.
        """
        self._on_error.append(hook)

    def on_close(self, hook: Callable) -> None:
        """Register a hook that fires when the session closes.

        Hook signature: ``async def hook(session)`` or ``def hook(session)``.
        Hooks run in registration order. Errors are logged but do not propagate.
        """
        self._on_close.append(hook)

    async def _fire_hooks(self, hooks: list[Callable], *args: Any) -> None:
        """Fire a list of hooks with the given arguments, logging and swallowing errors."""
        for hook in hooks:
            try:
                if inspect.iscoroutinefunction(hook):
                    await hook(*args)
                else:
                    hook(*args)
            except Exception:
                log.warning("lifecycle hook %s error", hook.__name__, exc_info=True)

    # --- Permission response (for consumer-handled requests) ---

    async def respond_allow(
        self,
        request_id: str,
        updated_input: dict,
        *,
        updated_permissions: list[dict] | None = None,
    ) -> None:
        """Allow a permission request that was surfaced to the consumer.

        ``updated_permissions`` optionally carries permission-rule updates to
        apply alongside the allow; it is omitted from the wire frame when None.
        """
        msg = AllowPermission(
            request_id=request_id,
            updated_input=updated_input,
            updated_permissions=updated_permissions,
        )
        await write_message(self._process_mgr.stdin, msg)

    async def respond_deny(self, request_id: str, message: str = "Denied by user") -> None:
        """Deny a permission request that was surfaced to the consumer."""
        msg = DenyPermission(request_id=request_id, message=message)
        await write_message(self._process_mgr.stdin, msg)

    async def respond_dialog(self, request_id: str, result: Any) -> None:
        """Complete a user dialog request with the user's chosen result.

        ``result`` is transported opaquely; its shape is defined per dialog_kind.
        """
        msg = DialogCompleted(request_id=request_id, result=result)
        await write_message(self._process_mgr.stdin, msg)

    async def respond_dialog_cancelled(self, request_id: str) -> None:
        """Cancel a user dialog request; the CLI applies the dialog's default behavior."""
        msg = DialogCancelled(request_id=request_id)
        await write_message(self._process_mgr.stdin, msg)

    # --- Public control methods ---

    async def interrupt(self, *, timeout: float = 30.0) -> list[str]:
        """Interrupt the running turn.

        Callable while a turn is active -- that is its purpose. Returns the list
        of user messages the CLI still had queued (empty on older CLIs that omit
        the field).
        """
        response = await self._control_request("interrupt", timeout=timeout)
        return response.get("still_queued", [])

    async def set_permission_mode(self, mode: str) -> None:
        """Change the permission mode mid-session.

        The mode string is passed through unvalidated; the CLI rejects unknown
        modes with an error response. On success the ``permission_mode`` property
        is updated to reflect the new mode.
        """
        await self._control_request("set_permission_mode", {"mode": mode})
        self._permission_mode = mode

    async def set_model(self, model: str | None) -> None:
        """Switch the model mid-session.

        Passing None omits the model field, resetting the CLI to its default. On
        success the ``model_name`` property is updated (None means the model is
        unknown until the next SystemInit reports it).
        """
        payload = {"model": model} if model is not None else {}
        await self._control_request("set_model", payload)
        self._model_name = model

    async def get_context_usage(self, *, timeout: float = 30.0) -> ContextUsage:
        """Query the model's current context-window usage.

        Raises ClaudeStreamError if the CLI response omits totalTokens/maxTokens.
        """
        response = await self._control_request("get_context_usage", timeout=timeout)
        if "totalTokens" not in response or "maxTokens" not in response:
            raise ClaudeStreamError(
                f"get_context_usage response missing totalTokens/maxTokens: {response!r}"
            )
        categories = [
            ContextCategory(name=c.get("name", ""), tokens=c.get("tokens", 0))
            for c in response.get("categories", [])
        ]
        return ContextUsage(
            total_tokens=response["totalTokens"],
            max_tokens=response["maxTokens"],
            percentage=response.get("percentage", 0.0),
            categories=categories,
            auto_compact_enabled=response.get("isAutoCompactEnabled", False),
            raw=response,
        )
