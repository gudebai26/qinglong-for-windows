#!/usr/bin/env python3
r"""Build the Windows package and publish it to GitHub.

    python scripts/release.py                  # build + publish v<ver>-go.N+1
    python scripts/release.py --dry-run        # build + zip, publish nothing
    python scripts/release.py --skip-build     # reuse dist\ as-is
    python scripts/release.py --tag v2.24.0-go.3 --notes notes.md

The archive contains exactly three files, matching what the panel expects:

    qinglong.exe       panel (embedded React build + vendored node_modules)
    qinglong-CLI.exe   terminal entry point (task / list / doctor / version)
    start.bat          launcher (GBK + CRLF)

README.txt stays in dist\ for local use but is not shipped: the release body
carries the same instructions.

The version comes from version.yaml; the build number is the highest
`v<version>-go.<n>` already published, plus one. The GitHub token is taken from
GITHUB_TOKEN when set, otherwise from the Windows credential manager entry that
`git push` already uses (`git credential fill`); it is never printed.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
import urllib.error
import urllib.parse
import urllib.request
import zipfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
DIST = ROOT / "dist"
SHIPPED = ("qinglong.exe", "qinglong-CLI.exe", "start.bat")


def say(message: str) -> None:
    print(message, flush=True)


def run(command: list[str], cwd: Path = ROOT, capture: bool = False) -> str:
    result = subprocess.run(
        command,
        cwd=cwd,
        check=False,
        text=True,
        encoding="utf-8",
        errors="replace",
        stdout=subprocess.PIPE if capture else None,
        stderr=subprocess.STDOUT if capture else None,
    )
    if result.returncode != 0:
        detail = (result.stdout or "").strip()
        raise SystemExit(f"command failed ({result.returncode}): {' '.join(command)}\n{detail}")
    return (result.stdout or "").strip()


def read_version() -> str:
    text = (ROOT / "version.yaml").read_text(encoding="utf-8")
    match = re.search(r"(?m)^version:\s*([0-9]+\.[0-9]+\.[0-9]+)\s*$", text)
    if not match:
        raise SystemExit("version.yaml has no `version: x.y.z` line")
    return match.group(1)


def repo_slug() -> tuple[str, str]:
    url = run(["git", "remote", "get-url", "origin"], capture=True)
    match = re.search(r"github\.com[:/]([^/]+)/([^/.]+?)(?:\.git)?$", url)
    if not match:
        raise SystemExit(f"cannot parse the GitHub repo from origin: {url}")
    return match.group(1), match.group(2)


def token() -> str:
    value = os.environ.get("GITHUB_TOKEN") or os.environ.get("GH_TOKEN")
    if value:
        return value.strip()
    # The credential manager already holds a token because `git push` works.
    helper = subprocess.run(
        ["git", "credential", "fill"],
        cwd=ROOT,
        input="protocol=https\nhost=github.com\n\n",
        text=True,
        capture_output=True,
        check=False,
    )
    for line in helper.stdout.splitlines():
        if line.startswith("password="):
            return line[len("password="):].strip()
    raise SystemExit("no GitHub token: set GITHUB_TOKEN or sign in for `git push` once")


def api(method: str, url: str, token_value: str, payload: dict | None = None,
        raw: bytes | None = None, content_type: str = "application/json",
        timeout: int = 60) -> dict:
    data = raw if raw is not None else (
        json.dumps(payload).encode("utf-8") if payload is not None else None
    )
    request = urllib.request.Request(url, data=data, method=method)
    request.add_header("Authorization", f"Bearer {token_value}")
    request.add_header("Accept", "application/vnd.github+json")
    request.add_header("X-GitHub-Api-Version", "2022-11-28")
    request.add_header("User-Agent", "qinglong-go-release")
    if data is not None:
        request.add_header("Content-Type", content_type)
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    try:
        with opener.open(request, timeout=timeout) as response:
            body = response.read()
            return json.loads(body) if body else {}
    except urllib.error.HTTPError as error:
        detail = error.read().decode("utf-8", "replace")
        raise SystemExit(f"GitHub API {method} {url} -> {error.code}\n{detail}") from None


def next_tag(owner: str, repo: str, token_value: str, version: str) -> tuple[str, str | None]:
    """Return the next `v<version>-go.N` tag and the tag it follows."""
    releases = api("GET",
                   f"https://api.github.com/repos/{owner}/{repo}/releases?per_page=100",
                   token_value)
    pattern = re.compile(rf"^v{re.escape(version)}-go\.(\d+)$")
    highest = 0
    previous = None
    for release in releases:
        tag = release.get("tag_name", "")
        match = pattern.match(tag)
        if not match:
            continue
        number = int(match.group(1))
        if number > highest:
            highest = number
            previous = tag
    return f"v{version}-go.{highest + 1}", previous


def build(skip: bool) -> None:
    if skip:
        say("skip build, reusing dist\\")
        return
    say("running scripts\\build.ps1 ...")
    run(["powershell", "-NoProfile", "-ExecutionPolicy", "Bypass",
         "-File", str(ROOT / "scripts" / "build.ps1")])
    for name in SHIPPED:
        if not (DIST / name).is_file():
            raise SystemExit(f"dist\\{name} is missing after the build")


def make_zip(version: str) -> Path:
    # One name for every build, on purpose: the release tag already carries the
    # build number, and a stable file name keeps download links and mirror
    # scripts from breaking on each release.
    target = ROOT / f"qinglong-{version}-win64.zip"
    if target.exists():
        target.unlink()
    with zipfile.ZipFile(target, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as archive:
        for name in SHIPPED:
            archive.write(DIST / name, name)
    with zipfile.ZipFile(target) as archive:
        names = archive.namelist()
    if sorted(names) != sorted(SHIPPED):
        raise SystemExit(f"{target.name} contains {names}, expected {list(SHIPPED)}")
    say(f"packed {target.name} ({target.stat().st_size / 1024 / 1024:.1f} MB): {', '.join(names)}")
    return target


# Commit prefixes are turned into the three buckets an operator cares about.
NOTE_GROUPS = {
    "feat": ("新增", 0),
    "fix": ("修复", 1),
    "perf": ("优化", 2),
    "refactor": ("改动", 2),
    "build": ("改动", 2),
    "docs": ("改动", 2),
    "test": ("改动", 2),
    "chore": ("改动", 2),
}
NOTE_ORDER = ("新增", "修复", "优化", "改动")


def release_notes(previous: str | None) -> str:
    """Render the release body: what was added, fixed or changed. Nothing else.

    The tag carries the build number and the asset name is fixed, so the body
    does not repeat the version, the file list or upgrade steps.
    """
    if previous:
        try:
            run(["git", "fetch", "--tags", "--quiet", "origin"], capture=True)
        except SystemExit:
            # Offline or blocked: fall back to the local tag, if any.
            pass
        try:
            history = run(["git", "log", "--no-merges", "--pretty=%s",
                           f"{previous}..HEAD"], capture=True)
        except SystemExit:
            history = ""
    else:
        history = run(["git", "log", "--no-merges", "--pretty=%s", "-12"], capture=True)

    buckets: dict[str, list[str]] = {}
    for line in history.splitlines():
        subject = line.strip()
        if not subject:
            continue
        prefix, _, rest = subject.partition(":")
        group = NOTE_GROUPS.get(prefix.strip().lower())
        if group and rest.strip():
            label, _ = group
            buckets.setdefault(label, []).append(rest.strip())
        else:
            buckets.setdefault("改动", []).append(subject)

    if not buckets:
        buckets["改动"] = ["维护版本，无功能变化"]

    blocks = []
    for label in NOTE_ORDER:
        items = buckets.get(label)
        if not items:
            continue
        blocks.append("### " + label + "\n\n" + "\n".join("- " + item for item in items))
    return "\n\n".join(blocks) + "\n"


def main() -> int:
    parser = argparse.ArgumentParser(description="build and publish a GitHub release")
    parser.add_argument("--skip-build", action="store_true", help="reuse dist\\ instead of rebuilding")
    parser.add_argument("--dry-run", action="store_true", help="build and zip, do not publish")
    parser.add_argument("--tag", help="explicit tag, e.g. v2.24.0-go.3")
    parser.add_argument("--notes", help="file with the release body (default: generated)")
    parser.add_argument("--allow-dirty", action="store_true",
                        help="publish even with uncommitted changes or unpushed commits")
    args = parser.parse_args()

    build(args.skip_build)

    version = read_version()
    owner, repo = repo_slug()

    if args.dry_run:
        tag = args.tag or f"v{version}-go.0"
        archive = make_zip(version)
        say(f"dry run: would publish {tag} with {archive.name}")
        return 0

    dirty = run(["git", "status", "--porcelain", "--untracked-files=no"], capture=True)
    ahead = run(["git", "rev-list", "--count", "origin/main..HEAD"], capture=True)
    if not args.allow_dirty and (dirty or ahead not in ("", "0")):
        detail = []
        if dirty:
            detail.append("uncommitted changes:\n" + dirty)
        if ahead not in ("", "0"):
            detail.append(f"{ahead} unpushed commit(s)")
        raise SystemExit("refusing to release: " + "; ".join(detail) + "\ncommit and push first, or pass --allow-dirty")

    token_value = token()
    if args.tag:
        tag = args.tag
        previous = None
    else:
        tag, previous = next_tag(owner, repo, token_value, version)

    archive = make_zip(version)

    if args.notes:
        body = Path(args.notes).read_text(encoding="utf-8")
    else:
        body = release_notes(previous)

    release = ensure_release(owner, repo, token_value, tag, body)
    asset = upload_asset(owner, repo, token_value, release, archive)
    say(f"published {release['html_url']}")
    say(f"asset    {asset.get('browser_download_url', archive.name)}")
    return 0


def ensure_release(owner: str, repo: str, token_value: str, tag: str, body: str) -> dict:
    """Create the release, or reuse the one a previous run left behind.

    An upload can fail halfway (a 23 MB asset over a slow link), leaving an
    empty release. Re-running the script must resume instead of dying on 422.
    """
    base = f"https://api.github.com/repos/{owner}/{repo}/releases"
    try:
        release = api("GET", f"{base}/tags/{urllib.parse.quote(tag)}", token_value)
        say(f"reusing release {tag} ({len(release.get('assets') or [])} asset(s))")
        return release
    except SystemExit as error:
        if "-> 404" not in str(error):
            raise
    say(f"creating release {tag} on {owner}/{repo} ...")
    return api("POST", base, token_value, {
        "tag_name": tag,
        "name": tag,
        "body": body,
        "draft": False,
        "prerelease": False,
    })


def upload_asset(owner: str, repo: str, token_value: str, release: dict, archive: Path) -> dict:
    """Upload the archive, retrying the slow ones.

    GitHub keeps an unfinished asset row when the connection drops, so a retry
    first clears the stale entry with the same name.
    """
    url = (f"https://uploads.github.com/repos/{owner}/{repo}/releases/"
           f"{release['id']}/assets?name={urllib.parse.quote(archive.name)}")
    data = archive.read_bytes()
    last: Exception | None = None
    for attempt in range(1, 4):
        existing = [a for a in (release.get("assets") or []) if a["name"] == archive.name]
        for asset in existing:
            if asset.get("size") == archive.stat().st_size and asset.get("state") == "uploaded":
                say(f"asset {archive.name} already uploaded")
                return asset
            say(f"clearing stale asset {archive.name} ({asset.get('state')})")
            api("DELETE", f"https://api.github.com/repos/{owner}/{repo}/releases/assets/{asset['id']}",
                token_value)
        say(f"uploading {archive.name} ({len(data) / 1024 / 1024:.1f} MB, attempt {attempt}/3) ...")
        try:
            return api("POST", url, token_value, raw=data, content_type="application/zip",
                       timeout=900)
        except Exception as error:  # URLError, timeout, HTTPError
            last = error
            say(f"upload failed: {error}")
            release = api("GET", f"https://api.github.com/repos/{owner}/{repo}/releases/tags/"
                         f"{urllib.parse.quote(release['tag_name'])}", token_value)
    raise SystemExit(f"upload failed after 3 attempts: {last}")


if __name__ == "__main__":
    if hasattr(sys.stdout, "reconfigure"):
        sys.stdout.reconfigure(encoding="utf-8", errors="replace")
    sys.exit(main())
