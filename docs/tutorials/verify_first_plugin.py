"""Extract and verify first-plugin.md; optional --ingot also builds/runs its Image.

Run from any directory. Output is retained for inspection; an existing output
directory must be empty. This never uses the user's normal Managed Home.
"""

from __future__ import annotations

import argparse
import os
from pathlib import Path
import re
import subprocess
import tempfile


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, help="new or empty scratch directory")
    parser.add_argument("--ingot", type=Path, help="current Core executable; enables Image verification")
    parser.add_argument("--sdk", type=Path, help="SDK checkout; default: beside plugins")
    parser.add_argument("--abi", type=Path, help="ABI checkout; default: beside plugins")
    args = parser.parse_args()
    repository = Path(__file__).resolve().parents[2]
    output = args.output.resolve() if args.output else Path(tempfile.mkdtemp(prefix="ingot-first-plugin-"))
    if output.exists() and any(output.iterdir()):
        parser.error(f"output must be empty: {output}")
    output.mkdir(parents=True, exist_ok=True)
    expected = {
        "tool-greet/go.mod", "tool-greet/ingot.plugin.toml", "tool-greet/greet.go",
        "tool-greet/greet_test.go", "demo-host/go.mod", "demo-host/ingot.plugin.toml",
        "demo-host/host.go", "go.work", "project/plugins.toml",
    }
    document = Path(__file__).with_name("first-plugin.md").read_text(encoding="utf-8")
    blocks = re.findall(r"<!-- tutorial-file: ([^\n]+) -->\s+```(?:go|toml)\n(.*?)\n```", document, re.S)
    if len(blocks) != len(expected) or {name for name, _ in blocks} != expected:
        raise SystemExit("tutorial markers must identify the nine expected complete files exactly once")
    sdk = (args.sdk or repository.parent / "sdk").resolve()
    abi = (args.abi or repository.parent / "ingot-abi").resolve()
    for name, code in blocks:
        if name == "go.work":
            code = code.replace("../plugins/tool-runtime", '"' + (repository / "tool-runtime").as_posix() + '"')
            code = code.replace("../sdk", '"' + sdk.as_posix() + '"')
            code = code.replace("../ingot-abi", '"' + abi.as_posix() + '"')
        if name == "project/plugins.toml":
            code = code.replace("../../plugins/tool-runtime", (repository / "tool-runtime").as_posix())
        target = output / name
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(code + "\n", encoding="utf-8")
    print(f"Extracted tutorial to {output}", flush=True)
    env = dict(os.environ, GOWORK="off")
    for module in ("tool-greet", "demo-host"):
        for command in (["go", "mod", "tidy"], ["go", "build", "./..."], ["go", "vet", "./..."], ["go", "test", "-v", "./..."]):
            subprocess.run(command, cwd=output / module, env=env, check=True)
    if args.ingot:
        executable = str(args.ingot.resolve())
        for checkout in (sdk, abi):
            if not (checkout / "go.mod").is_file():
                raise SystemExit(f"missing contract checkout: {checkout}")
        command = [executable, "--home", str(output / ".ingot-demo")]
        for tail in (["setup"], ["project", "resolve", "-f", "project/plugins.toml"], ["build", "greeting", "-f", "project/plugins.toml", "--locked"]):
            subprocess.run(command + tail, cwd=output, env=env, check=True)
        completed = subprocess.run(command + ["start", "greeting", "--foreground"], cwd=output, env=env, check=True, text=True, capture_output=True, timeout=60)
        print(completed.stdout, end="")
        for expected_line in ("Hello, Ingot!", "invalid input rejected before dispatch"):
            if expected_line not in completed.stdout.splitlines():
                raise SystemExit(f"missing expected output: {expected_line}")
    print("Tutorial checks passed. Scratch files were retained for review.")


if __name__ == "__main__":
    main()
