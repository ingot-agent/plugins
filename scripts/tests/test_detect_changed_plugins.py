from __future__ import annotations

import sys
import unittest
from pathlib import Path


SCRIPTS_DIR = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS_DIR))

from detect_changed_plugins import matrix_for, select_changed_plugins


AVAILABLE_PLUGINS = {"agent-default", "tool-edit", "tool-shell"}


class SelectChangedPluginsTests(unittest.TestCase):
    def test_repository_only_change_selects_no_plugins(self) -> None:
        self.assertEqual(select_changed_plugins(["README.md"], AVAILABLE_PLUGINS), [])

    def test_single_plugin_change(self) -> None:
        self.assertEqual(
            select_changed_plugins(["tool-shell/main.go"], AVAILABLE_PLUGINS),
            ["tool-shell"],
        )

    def test_multiple_plugin_changes_are_sorted(self) -> None:
        self.assertEqual(
            select_changed_plugins(
                ["tool-shell/main.go", "tool-edit/go.mod"], AVAILABLE_PLUGINS
            ),
            ["tool-edit", "tool-shell"],
        )

    def test_duplicate_files_are_deduplicated(self) -> None:
        self.assertEqual(
            select_changed_plugins(
                ["tool-shell/main.go", "tool-shell/CHANGELOG.md"],
                AVAILABLE_PLUGINS,
            ),
            ["tool-shell"],
        )


class MatrixTests(unittest.TestCase):
    def test_no_changed_plugins_produces_noop_matrix(self) -> None:
        self.assertEqual(
            matrix_for([]),
            {
                "include": [
                    {
                        "plugin": ".",
                        "label": "no changed plugins",
                        "run_tests": False,
                    }
                ]
            },
        )

    def test_changed_plugins_produce_parallel_matrix_entries(self) -> None:
        self.assertEqual(
            matrix_for(["tool-edit", "tool-shell"]),
            {
                "include": [
                    {
                        "plugin": "tool-edit",
                        "label": "tool-edit",
                        "run_tests": True,
                    },
                    {
                        "plugin": "tool-shell",
                        "label": "tool-shell",
                        "run_tests": True,
                    },
                ]
            },
        )


if __name__ == "__main__":
    unittest.main()
