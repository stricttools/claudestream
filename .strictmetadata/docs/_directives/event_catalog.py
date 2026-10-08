"""selfdoc custom directive: render the event type catalog from events.py.

Registered in selfdoc.json as ``"table-events"``. selfdoc loads this file
and calls ``resolve(attrs, config, body) -> str``; the returned markdown
replaces the ``:-: table-events`` directive line.

The source of truth is ``claudestream/events.py``. This directive parses
the AST of that file to extract every ``Event`` subclass name and its
docstring, so the generated table can never drift from the code.
"""

from __future__ import annotations

import ast
import os


def _find_event_classes(source: str) -> list[tuple[str, str]]:
    """Extract (class_name, first_sentence_of_docstring) for Event subclasses.

    Walks the AST looking for class definitions that inherit from ``Event``
    (directly or transitively via another class that inherits from Event).
    Skips the ``Event`` base class itself and non-Event structs (content
    blocks, Usage, etc.).
    """
    tree = ast.parse(source)

    # First pass: build a set of class names that are Event subclasses.
    # Start with "Event" itself; any class inheriting from a known Event
    # class is also an Event class.
    event_classes: set[str] = {"Event"}
    # We need multiple passes because a subclass may appear before its
    # base in the file (unlikely but safe to handle).
    changed = True
    class_nodes: dict[str, ast.ClassDef] = {}
    for node in ast.walk(tree):
        if isinstance(node, ast.ClassDef):
            class_nodes[node.name] = node

    while changed:
        changed = False
        for name, node in class_nodes.items():
            if name in event_classes:
                continue
            for base in node.bases:
                base_name = base.id if isinstance(base, ast.Name) else None
                if base_name in event_classes:
                    event_classes.add(name)
                    changed = True
                    break

    # Second pass: collect results in source order, skipping the base.
    results: list[tuple[str, str]] = []
    for node in ast.iter_child_nodes(tree):
        if not isinstance(node, ast.ClassDef):
            continue
        if node.name not in event_classes or node.name == "Event":
            continue
        docstring = ast.get_docstring(node) or ""
        # Take the first line of the docstring as the description.
        first_line = docstring.split("\n")[0].strip()
        results.append((node.name, first_line))

    return results


def _repository_root():
    """The directory holding selfdoc.json, found by walking up from this
    file, so the directive works wherever the docs directory lives."""
    here = os.path.dirname(os.path.abspath(__file__))
    while True:
        if os.path.exists(os.path.join(here, "selfdoc.json")):
            return here
        parent = os.path.dirname(here)
        if parent == here:
            raise FileNotFoundError("no selfdoc.json above " + __file__)
        here = parent


def resolve(attrs, config, body):
    """Return a markdown table of event types parsed from events.py."""
    repo_root = _repository_root()
    events_path = os.path.join(repo_root, "claudestream", "events.py")

    with open(events_path) as f:
        source = f.read()

    classes = _find_event_classes(source)
    if not classes:
        raise RuntimeError(f"no Event subclasses found in {events_path}")

    lines = [
        "| Event Type | Description |",
        "| --- | --- |",
    ]
    for name, description in classes:
        lines.append(f"| `{name}` | {description} |")

    return "\n".join(lines)
