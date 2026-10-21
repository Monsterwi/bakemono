#!/usr/bin/env python3
"""Build hey v0.1.4 with actual body byte/error accounting in an isolated directory."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    output = args.output.resolve()
    output.parent.mkdir(parents=True, exist_ok=True)
    env = dict(os.environ, GOWORK="off", CGO_ENABLED="0")
    with tempfile.TemporaryDirectory(prefix="razor-hey-build-") as directory:
        module = json.loads(subprocess.check_output(
            ["go", "mod", "download", "-json", "github.com/rakyll/hey@v0.1.4"],
            cwd=directory, env=env, text=True))
        source = Path(directory) / "source"
        shutil.copytree(module["Dir"], source, copy_function=shutil.copyfile)
        for current, _, _ in os.walk(source):
            os.chmod(current, 0o755)
        path = source / "requester/requester.go"
        text = path.read_text()
        original = "\t\tio.Copy(ioutil.Discard, resp.Body)"
        replacement = "\t\tsize, err = io.Copy(ioutil.Discard, resp.Body)"
        assert text.count(original) == 1, "unexpected upstream source; do not apply an unchecked patch"
        path.write_text(text.replace(original, replacement))
        subprocess.run(["go", "build", "-mod=readonly", "-o", str(output), "."], cwd=source, env=env, check=True)
    metadata = {"upstream": "github.com/rakyll/hey@v0.1.4", "module_sum": module["Sum"],
                "variant": "razor-bodycheck", "patch": {"before": original, "after": replacement},
                "sha256": hashlib.sha256(output.read_bytes()).hexdigest(),
                "go": subprocess.check_output(["go", "version"], text=True).strip()}
    output.with_suffix(".build.json").write_text(json.dumps(metadata, indent=2) + "\n")
    print(json.dumps(metadata), flush=True)


if __name__ == "__main__":
    main()
