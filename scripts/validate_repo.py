#!/usr/bin/env python3
"""Validate the repository contract for first-level Ingot plugin modules."""

from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

try:
    import tomllib
except ModuleNotFoundError:  # pragma: no cover - CI uses Python 3.12+
    tomllib = None


REPO_ROOT = Path(__file__).resolve().parent.parent
PLUGIN_MODULE_PREFIX = "github.com/ingot-agent/plugins/"
CORE_MODULE = "github.com/ingot-agent/ingot"
INFRASTRUCTURE_TOP_LEVEL_DIRS = {".github", "docs", "scripts", "tools"}
LOCAL_METADATA_DIRS = {".git", ".idea", ".vscode"}
PLUGIN_MARKER_FILES = ("go.mod", "ingot.plugin.toml")

REQUIRED_ROOT_FILES = (
    ".github/workflows/ci.yml",
    ".github/pull_request_template.md",
    ".gitignore",
    "CONTRIBUTING.md",
    "LICENSE",
    "README.md",
    "RELEASE.md",
    "go.work",
    "scripts/detect_changed_plugins.py",
    "scripts/tests/test_detect_changed_plugins.py",
    "scripts/tests/test_validate_repo.py",
    "scripts/validate_repo.py",
)


def plugin_dirs(repo_root: Path) -> list[Path]:
    """Return first-level directories carrying both plugin marker files."""

    non_plugin_names = INFRASTRUCTURE_TOP_LEVEL_DIRS | LOCAL_METADATA_DIRS
    return sorted(
        (
            path
            for path in repo_root.iterdir()
            if path.is_dir()
            and path.name not in non_plugin_names
            and all((path / marker).is_file() for marker in PLUGIN_MARKER_FILES)
        ),
        key=lambda path: path.name,
    )


def unexpected_top_level_dirs(repo_root: Path) -> list[Path]:
    """Return directories that are neither infrastructure nor plugins."""

    plugins = set(plugin_dirs(repo_root))
    allowed_names = INFRASTRUCTURE_TOP_LEVEL_DIRS | LOCAL_METADATA_DIRS
    return sorted(
        (
            path
            for path in repo_root.iterdir()
            if path.is_dir()
            and path not in plugins
            and path.name not in allowed_names
        ),
        key=lambda path: path.name,
    )


def strip_go_comment(line: str) -> str:
    return line.split("//", 1)[0].strip()


def module_path(go_mod: Path) -> str | None:
    for raw_line in go_mod.read_text(encoding="utf-8").splitlines():
        line = strip_go_comment(raw_line)
        match = re.match(r"^module\s+(\S+)$", line)
        if match:
            return match.group(1)
    return None


def required_module_paths(go_mod: Path) -> list[str]:
    """Extract module paths from single-line and block require declarations."""

    dependencies: list[str] = []
    in_require_block = False

    for raw_line in go_mod.read_text(encoding="utf-8").splitlines():
        line = strip_go_comment(raw_line)
        if not line:
            continue

        if in_require_block:
            if line == ")":
                in_require_block = False
                continue
            fields = line.split()
            if fields:
                dependencies.append(fields[0])
            continue

        require_match = re.match(r"^require\s*(.*)$", line)
        if not require_match:
            continue

        remainder = require_match.group(1).strip()
        if remainder == "(":
            in_require_block = True
            continue
        fields = remainder.split()
        if fields:
            dependencies.append(fields[0])

    return dependencies


def is_core_dependency(path: str) -> bool:
    return path == CORE_MODULE or path.startswith(f"{CORE_MODULE}/")


def is_sibling_plugin_dependency(path: str, own_module: str) -> bool:
    if not path.startswith(PLUGIN_MODULE_PREFIX):
        return False
    return path != own_module and not path.startswith(f"{own_module}/")


def validate_workspace(
    repo_root: Path, plugins: list[Path], errors: list[str]
) -> None:
    go_work = repo_root / "go.work"
    if not go_work.is_file():
        errors.append("missing required file: go.work")
        return

    go_versions: list[tuple[int, str]] = []
    use_paths: list[tuple[int, str]] = []
    in_use_block = False

    for line_number, raw_line in enumerate(
        go_work.read_text(encoding="utf-8").splitlines(), start=1
    ):
        line = strip_go_comment(raw_line)
        if not line:
            continue

        if in_use_block:
            if line == ")":
                in_use_block = False
            else:
                use_paths.append((line_number, line))
            continue

        go_match = re.fullmatch(r"go\s+(\S+)", line)
        if go_match:
            go_versions.append((line_number, go_match.group(1)))
            continue

        if line == "use (":
            in_use_block = True
            continue

        use_match = re.fullmatch(r"use\s+(\S+)", line)
        if use_match:
            use_paths.append((line_number, use_match.group(1)))
            continue

        errors.append(f"go.work:{line_number}: unsupported directive or syntax: {line}")

    if in_use_block:
        errors.append("go.work: unterminated use block")

    if len(go_versions) != 1 or go_versions[0][1] != "1.24.2":
        errors.append("go.work must declare exactly one Go version: go 1.24.2")

    plugin_names = {plugin.name for plugin in plugins}
    seen_use_paths: set[str] = set()
    for line_number, use_path in use_paths:
        if not re.fullmatch(r"\./[^/\\]+", use_path):
            errors.append(
                f"go.work:{line_number}: use path must reference one first-level "
                f"plugin as ./<plugin>, found {use_path}"
            )
            continue

        plugin_name = use_path[2:]
        if plugin_name in {"", ".", ".."} or plugin_name not in plugin_names:
            errors.append(
                f"go.work:{line_number}: use path does not reference a current "
                f"first-level plugin module: {use_path}"
            )
            continue

        if use_path in seen_use_paths:
            errors.append(f"go.work:{line_number}: duplicate use path: {use_path}")
        seen_use_paths.add(use_path)


def validate_plugin(plugin_dir: Path, errors: list[str], manifest_names: dict[str, Path]) -> None:
    plugin_name = plugin_dir.name
    go_mod = plugin_dir / "go.mod"
    manifest = plugin_dir / "ingot.plugin.toml"
    changelog = plugin_dir / "CHANGELOG.md"

    for required_file in (go_mod, manifest, changelog):
        if not required_file.is_file():
            errors.append(f"{plugin_name}: missing required file {required_file.name}")

    if not go_mod.is_file():
        return

    declared_module = module_path(go_mod)
    expected_module = f"{PLUGIN_MODULE_PREFIX}{plugin_name}"
    if declared_module != expected_module:
        actual = declared_module or "<missing>"
        errors.append(
            f"{plugin_name}: module path must be {expected_module}, found {actual}"
        )

    go_mod_text = go_mod.read_text(encoding="utf-8")
    if re.search(r"(?m)^\s*replace(?:\s|\()", go_mod_text):
        errors.append(f"{plugin_name}: go.mod must not contain replace directives")

    own_module = declared_module or expected_module
    for dependency in required_module_paths(go_mod):
        if is_core_dependency(dependency):
            errors.append(
                f"{plugin_name}: go.mod must not depend on {CORE_MODULE}"
            )
        elif is_sibling_plugin_dependency(dependency, own_module):
            errors.append(
                f"{plugin_name}: go.mod must not depend on sibling plugin {dependency}"
            )

    if not manifest.is_file():
        return

    if tomllib is None:  # pragma: no cover - retained for clear local failure
        errors.append(f"{plugin_name}: Python 3.11+ is required to parse TOML")
        return

    try:
        with manifest.open("rb") as manifest_file:
            document = tomllib.load(manifest_file)
    except (OSError, tomllib.TOMLDecodeError) as exc:
        errors.append(f"{plugin_name}: invalid ingot.plugin.toml ({exc})")
        return

    name = document.get("name")
    if not isinstance(name, str) or not name.strip():
        errors.append(f"{plugin_name}: ingot.plugin.toml must define a non-empty name")
        return

    if name in manifest_names:
        errors.append(
            f"manifest.name {name!r} is duplicated by {manifest_names[name].parent.name}"
        )
    else:
        manifest_names[name] = manifest


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--list-plugins",
        action="store_true",
        help="print first-level plugin directories, one per line",
    )
    args = parser.parse_args()

    discovered_plugins = plugin_dirs(REPO_ROOT)
    if args.list_plugins:
        for plugin_dir in discovered_plugins:
            print(plugin_dir.name)
        return 0

    errors: list[str] = []
    for required_file in REQUIRED_ROOT_FILES:
        if not (REPO_ROOT / required_file).is_file():
            errors.append(f"missing required file: {required_file}")

    for unexpected_dir in unexpected_top_level_dirs(REPO_ROOT):
        errors.append(
            f"unexpected top-level directory {unexpected_dir.name}: directories must "
            "be declared infrastructure or contain go.mod and ingot.plugin.toml"
        )

    validate_workspace(REPO_ROOT, discovered_plugins, errors)

    manifest_names: dict[str, Path] = {}
    for plugin_dir in discovered_plugins:
        validate_plugin(plugin_dir, errors, manifest_names)

    if errors:
        print("Repository validation failed:", file=sys.stderr)
        for error in errors:
            print(f"- {error}", file=sys.stderr)
        return 1

    if not discovered_plugins:
        print("No plugin directories found; bootstrap repository validation passed.")
        return 0

    print(f"Repository validation passed for {len(discovered_plugins)} plugin(s).")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
