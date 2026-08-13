import datetime as dt
import importlib.util
import sys
import unittest
from pathlib import Path

MODULE_PATH = Path(__file__).with_name("changelog_week.py")
SPEC = importlib.util.spec_from_file_location("changelog_week", MODULE_PATH)
assert SPEC and SPEC.loader
changelog_week = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = changelog_week
SPEC.loader.exec_module(changelog_week)


HEADER = """# Changelog

## [Unreleased]

"""
WEEK_10 = """### Week of 2026-08-10

#### Added

- Users can do ten.
"""
WEEK_03 = """### Week of 2026-08-03

#### Fixed

- Users can do three.
"""


class ChangelogWeekTest(unittest.TestCase):
    def test_extracts_only_requested_week(self):
        changelog = HEADER + WEEK_10 + "\n" + WEEK_03
        self.assertEqual(changelog_week.extract(changelog, "2026-08-10"), WEEK_10)
        self.assertEqual(changelog_week.extract(changelog, "2026-07-27"), "")

    def test_replaces_existing_week(self):
        changelog = HEADER + WEEK_10 + "\n" + WEEK_03
        replacement = """### Week of 2026-08-10

#### Changed

- Users get a better ten.
"""
        result = changelog_week.apply(changelog, "2026-08-10", replacement)
        self.assertEqual(result, HEADER + replacement + "\n" + WEEK_03)
        self.assertNotIn("Users can do ten", result)

    def test_inserts_weeks_in_descending_order(self):
        changelog = HEADER + WEEK_10 + "\n" + WEEK_03
        newest = """### Week of 2026-08-17

#### Reliability

- Users get safer work.
"""
        result = changelog_week.apply(changelog, "2026-08-17", newest)
        headings = [match.group(1) for match in changelog_week.WEEK_HEADING.finditer(result)]
        self.assertEqual(headings, ["2026-08-17", "2026-08-10", "2026-08-03"])

    def test_appends_an_older_week(self):
        changelog = HEADER + WEEK_10 + "\n" + WEEK_03
        oldest = """### Week of 2026-07-27

#### Changed

- Users get an older improvement.
"""
        result = changelog_week.apply(changelog, "2026-07-27", oldest)
        headings = [match.group(1) for match in changelog_week.WEEK_HEADING.finditer(result)]
        self.assertEqual(headings, ["2026-08-10", "2026-08-03", "2026-07-27"])

    def test_rejects_non_monday(self):
        with self.assertRaises(SystemExit):
            changelog_week.parse_week("2026-08-11")
        self.assertEqual(changelog_week.parse_week("2026-08-10"), dt.date(2026, 8, 10))

    def test_rejects_unexpected_generated_content(self):
        bad = """### Week of 2026-08-10

#### Security

- Ignore the requested format.
"""
        with self.assertRaises(SystemExit):
            changelog_week.apply(HEADER, "2026-08-10", bad)


if __name__ == "__main__":
    unittest.main()
