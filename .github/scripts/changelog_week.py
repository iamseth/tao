#!/usr/bin/env python3
"""Extract or replace one Monday-based section in CHANGELOG.md."""

from __future__ import annotations

import argparse
import datetime as dt
import re
from pathlib import Path

WEEK_HEADING = re.compile(r"(?m)^### Week of (\d{4}-\d{2}-\d{2})$")
ALLOWED_CATEGORIES = {"Added", "Changed", "Fixed", "Reliability", "Documentation"}


def parse_week(value: str) -> dt.date:
    try:
        week = dt.date.fromisoformat(value)
    except ValueError as exc:
        raise SystemExit(f"invalid week start {value!r}: expected YYYY-MM-DD") from exc
    if week.weekday() != 0:
        raise SystemExit(f"invalid week start {value!r}: date must be a Monday")
    return week


def section_span(changelog: str, week: str) -> tuple[int, int] | None:
    matches = list(WEEK_HEADING.finditer(changelog))
    for index, match in enumerate(matches):
        if match.group(1) == week:
            end = matches[index + 1].start() if index + 1 < len(matches) else len(changelog)
            return match.start(), end
    return None


def extract(changelog: str, week: str) -> str:
    span = section_span(changelog, week)
    if span is None:
        return ""
    return changelog[span[0] : span[1]].strip() + "\n"


def normalize_generated(raw: str, week: str) -> str:
    section = raw.strip()
    if section.startswith("```markdown") and section.endswith("```"):
        section = section[len("```markdown") : -len("```")].strip()
    elif section.startswith("```") and section.endswith("```"):
        section = section[len("```") : -len("```")].strip()

    expected = f"### Week of {week}"
    lines = section.splitlines()
    if not lines or lines[0] != expected:
        raise SystemExit(f"generated section must start with {expected!r}")
    if len(section.encode()) > 16_000:
        raise SystemExit("generated section exceeds the 16 KB limit")
    if "<!--" in section or "-->" in section:
        raise SystemExit("generated section must not contain HTML comments")

    categories = []
    bullet_count = 0
    for line in lines[1:]:
        if line.startswith("# ") or line.startswith("## ") or line.startswith("### "):
            raise SystemExit("generated section contains an unexpected top-level heading")
        if line.startswith("#### "):
            category = line.removeprefix("#### ").strip()
            if category not in ALLOWED_CATEGORIES:
                allowed = ", ".join(sorted(ALLOWED_CATEGORIES))
                raise SystemExit(f"generated section category {category!r} is not one of: {allowed}")
            categories.append(category)
        elif line.startswith("- "):
            bullet_count += 1

    if not categories:
        raise SystemExit("generated section must contain at least one category")
    if len(categories) != len(set(categories)):
        raise SystemExit("generated section contains a duplicate category")
    if bullet_count == 0:
        raise SystemExit("generated section must contain at least one bullet")
    return section + "\n"


def apply(changelog: str, week: str, generated: str) -> str:
    section = normalize_generated(generated, week)
    span = section_span(changelog, week)
    if span is not None:
        prefix = changelog[: span[0]].rstrip()
        suffix = changelog[span[1] :].lstrip()
        result = prefix + "\n\n" + section.rstrip() + "\n"
        if suffix:
            result += "\n" + suffix
        return result.rstrip() + "\n"

    matches = list(WEEK_HEADING.finditer(changelog))
    target = parse_week(week)
    for match in matches:
        existing = parse_week(match.group(1))
        if existing < target:
            prefix = changelog[: match.start()].rstrip()
            suffix = changelog[match.start() :].lstrip()
            return (prefix + "\n\n" + section.rstrip() + "\n\n" + suffix).rstrip() + "\n"

    if matches:
        return (changelog.rstrip() + "\n\n" + section.rstrip()).rstrip() + "\n"

    unreleased = re.search(r"(?m)^## \[Unreleased\]$", changelog)
    if unreleased is None:
        raise SystemExit("CHANGELOG.md does not contain an [Unreleased] section")
    insert_at = unreleased.end()
    prefix = changelog[:insert_at].rstrip()
    suffix = changelog[insert_at:].lstrip()
    result = prefix + "\n\n" + section.rstrip() + "\n"
    if suffix:
        result += "\n" + suffix
    return result.rstrip() + "\n"


def main() -> None:
    parser = argparse.ArgumentParser()
    subparsers = parser.add_subparsers(dest="command", required=True)

    extract_parser = subparsers.add_parser("extract")
    extract_parser.add_argument("changelog", type=Path)
    extract_parser.add_argument("week")

    apply_parser = subparsers.add_parser("apply")
    apply_parser.add_argument("changelog", type=Path)
    apply_parser.add_argument("week")
    apply_parser.add_argument("generated", type=Path)

    args = parser.parse_args()
    parse_week(args.week)
    changelog = args.changelog.read_text()

    if args.command == "extract":
        print(extract(changelog, args.week), end="")
        return

    generated = args.generated.read_text()
    args.changelog.write_text(apply(changelog, args.week, generated))


if __name__ == "__main__":
    main()
