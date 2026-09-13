from __future__ import annotations

import sys
import tempfile
import unittest
from pathlib import Path


SCRIPTS_DIR = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS_DIR))

from validate_repo import plugin_dirs, unexpected_top_level_dirs, validate_workspace


class WorkspaceValidationTests(unittest.TestCase):
    def validate(self, content: str, plugin_names: tuple[str, ...] = ()) -> list[str]:
        with tempfile.TemporaryDirectory() as temporary_dir:
            repo_root = Path(temporary_dir)
            (repo_root / "go.work").write_text(content, encoding="utf-8")
            plugins = [repo_root / plugin_name for plugin_name in plugin_names]
            errors: list[str] = []
            validate_workspace(repo_root, plugins, errors)
            return errors

    def test_go_version_without_use_is_valid(self) -> None:
        self.assertEqual(self.validate("go 1.24.2\n"), [])

    def test_first_level_plugin_use_block_is_valid(self) -> None:
        self.assertEqual(
            self.validate(
                "go 1.24.2\n\nuse (\n\t./tool-edit\n\t./tool-shell\n)\n",
                ("tool-edit", "tool-shell"),
            ),
            [],
        )

    def test_parent_external_nested_and_unknown_paths_are_rejected(self) -> None:
        errors = self.validate(
            "go 1.24.2\n\nuse (\n"
            "\t../ingot\n"
            "\tD:/external/plugin\n"
            "\t./group/tool-shell\n"
            "\t./unknown\n"
            ")\n",
            ("tool-shell",),
        )
        self.assertEqual(len(errors), 4)

    def test_duplicate_use_is_rejected(self) -> None:
        errors = self.validate(
            "go 1.24.2\nuse ./tool-shell\nuse ./tool-shell\n",
            ("tool-shell",),
        )
        self.assertEqual(len(errors), 1)
        self.assertIn("duplicate use path", errors[0])


class TopLevelDirectoryTests(unittest.TestCase):
    def test_plugins_require_both_marker_files_and_unknown_dirs_fail(self) -> None:
        with tempfile.TemporaryDirectory() as temporary_dir:
            repo_root = Path(temporary_dir)
            (repo_root / "scripts").mkdir()
            (repo_root / "docs").mkdir()
            tools = repo_root / "tools"
            tools.mkdir()
            (tools / "go.mod").touch()
            (tools / "ingot.plugin.toml").touch()

            plugin = repo_root / "tool-shell"
            plugin.mkdir()
            (plugin / "go.mod").touch()
            (plugin / "ingot.plugin.toml").touch()

            unknown = repo_root / "examples"
            unknown.mkdir()

            self.assertEqual(
                [path.name for path in plugin_dirs(repo_root)], ["tool-shell"]
            )
            self.assertEqual(
                [path.name for path in unexpected_top_level_dirs(repo_root)],
                ["examples"],
            )


if __name__ == "__main__":
    unittest.main()
