#!/usr/bin/env python3
"""Fail when the deployable console stops being a standalone static SPA."""

from __future__ import annotations

import argparse
import json
import re
import subprocess
from pathlib import Path


ROOT = Path(__file__).resolve().parent.parent
WEB = ROOT / "web"
SOURCE_SUFFIXES = {".js", ".jsx", ".mjs", ".ts", ".tsx"}
SERVER_IMPORT = re.compile(
    r"(?:from\s*|import\s*\(\s*|require\s*\(\s*)['\"](?:node:)?"
    r"(?:fs|net|tls|http|https|child_process|cluster|dgram|worker_threads)(?:/[^'\"]*)?['\"]"
)
NETWORK_CALL = (
    r"(?:\b(?:fetch|Request|WebSocket|EventSource|axios\.(?:get|post|put|patch|delete))"
    r"\s*\(\s*|\b(?:navigator\s*\.\s*)?sendBeacon\s*\(\s*|"
    r"\bXMLHttpRequest\s*\.\s*open\s*\(\s*[^,]+,\s*)"
)
ABSOLUTE_REQUEST = re.compile(NETWORK_CALL + r"['\"`]\s*(?:https?:)?//", re.I)
NON_API_ROOT_REQUEST = re.compile(NETWORK_CALL + r"['\"`]\s*/(?!api/)", re.I)


def resolved_vite_config() -> dict[str, object]:
    """Ask Vite for its effective build config instead of parsing TypeScript."""
    try:
        result = subprocess.run(
            ["node", str(ROOT / "scripts" / "read-vite-config.mjs")],
            cwd=ROOT,
            check=False,
            capture_output=True,
            text=True,
        )
    except OSError as error:
        raise RuntimeError(f"cannot start Node.js: {error}") from error
    if result.returncode != 0:
        detail = result.stderr.strip() or f"Node.js exited with {result.returncode}"
        raise RuntimeError(detail)
    return json.loads(result.stdout)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--dist", action="store_true", help="also require the built static artifact")
    args = parser.parse_args()
    errors: list[str] = []

    manifest = json.loads((WEB / "package.json").read_text())
    if not manifest.get("scripts", {}).get("build"):
        errors.append("web/package.json must define a static build script")
    dependencies = {**manifest.get("dependencies", {}), **manifest.get("devDependencies", {})}
    if "vite" not in dependencies:
        errors.append("web/package.json must declare the Vite static build boundary")

    try:
        vite = resolved_vite_config()
    except (RuntimeError, json.JSONDecodeError) as error:
        errors.append(f"cannot resolve vite.config.ts: {error}")
    else:
        if vite.get("base") != "/":
            errors.append("Vite base must be /")
        if Path(str(vite.get("root"))).resolve() != WEB.resolve():
            errors.append("Vite root must be web/")
        if Path(str(vite.get("outDir"))).resolve() != (ROOT / "dist" / "console").resolve():
            errors.append("Vite output must be isolated in dist/console")
        if vite.get("proxyKeys") != ["/api"]:
            errors.append("Vite development proxy must expose only /api")

    for source in WEB.rglob("*"):
        if not source.is_file() or source.suffix not in SOURCE_SUFFIXES:
            continue
        if "node_modules" in source.parts or source.name.endswith((".test.js", ".test.mjs", ".test.ts", ".test.tsx")):
            continue
        text = source.read_text(errors="replace")
        relative = source.relative_to(ROOT)
        if SERVER_IMPORT.search(text):
            errors.append(f"{relative}: imports a server-only module")
        if "process.env" in text or "import.meta.env" in text:
            errors.append(f"{relative}: reads a private runtime environment variable")
        if ABSOLUTE_REQUEST.search(text):
            errors.append(f"{relative}: sends a request to an absolute origin")
        if NON_API_ROOT_REQUEST.search(text):
            errors.append(f"{relative}: sends a request outside /api/")
        if re.search(r"\b(?:getServerSideProps|getInitialProps|server action)\b", text, re.I):
            errors.append(f"{relative}: contains a server-rendering primitive")

    if args.dist:
        output = ROOT / "dist" / "console"
        if not (output / "index.html").is_file():
            errors.append("dist/console/index.html is missing; run npm run build")
        assets = output / "assets"
        if not assets.is_dir() or not any(assets.iterdir()):
            errors.append("dist/console/assets is missing or empty")

    if errors:
        for error in errors:
            print(f"BLOCKER: {error}")
        return 1
    print("static console check: publish_allowed=true")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
