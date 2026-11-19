"""Shared source identity and durable records for the repository's agent tools."""

import datetime
import hashlib
import json
import os
from pathlib import Path
import stat
import subprocess
import tempfile


class WorkflowError(Exception):
    pass


def git(root, *args):
    result = subprocess.run(["git", *args], cwd=root, capture_output=True)
    if result.returncode:
        raise WorkflowError(result.stderr.decode(errors="replace").strip())
    return result.stdout


def roots():
    checkout = Path(os.fsdecode(git(Path.cwd(), "rev-parse", "--show-toplevel")).strip())
    entries = git(checkout, "worktree", "list", "--porcelain", "-z").split(b"\0")
    common = next(item[len(b"worktree "):] for item in entries if item.startswith(b"worktree "))
    return checkout.resolve(), Path(os.fsdecode(common)).resolve()


def now():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


def write_json(path, value):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    fd, temporary = tempfile.mkstemp(dir=path.parent, prefix=".record-")
    try:
        with os.fdopen(fd, "w") as stream:
            json.dump(value, stream, indent=2, sort_keys=True)
            stream.write("\n")
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def source_state(root):
    paths = set(git(root, "ls-files", "--cached", "--others", "--exclude-standard", "-z").split(b"\0"))
    digest = hashlib.sha256()
    for relative in sorted(paths - {b""}):
        path = root / os.fsdecode(relative)
        digest.update(relative + b"\0")
        if not path.exists() and not path.is_symlink():
            digest.update(b"deleted\0")
        elif path.is_symlink():
            digest.update(b"symlink\0" + os.fsencode(os.readlink(path)) + b"\0")
        elif path.is_file():
            executable = bool(path.stat().st_mode & (stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH))
            digest.update(b"executable\0" if executable else b"file\0")
            data = path.read_bytes()
            digest.update(len(data).to_bytes(8, "big") + data)
        else:
            raise WorkflowError(f"unsupported source path: {relative!r}")
    return {
        "commit": git(root, "rev-parse", "HEAD").decode().strip(),
        "source_digest": digest.hexdigest(),
        "dirty": bool(git(root, "status", "--porcelain", "--untracked-files=all")),
    }


def verification_environment():
    keys = ("GOFLAGS", "GOOS", "GOARCH", "CGO_ENABLED", "GODEBUG", "GOTOOLCHAIN", "GOMAXPROCS", "TZ")
    # Values can contain local paths or credentials; retain only their fingerprint.
    values = {key: os.environ.get(key, "") for key in keys}
    return hashlib.sha256(json.dumps(values, sort_keys=True).encode()).hexdigest()


def verification(root, common):
    source = source_state(root)
    try:
        toolchain = subprocess.check_output(["go", "version"], text=True).strip()
    except (OSError, subprocess.CalledProcessError):
        return {"status": "unverified", "reason": "Go toolchain unavailable"}
    records = common / ".scratch" / "checks"
    matches = []
    for path in records.glob("*/result.json"):
        try:
            record = json.loads(path.read_text())
            if not isinstance(record, dict):
                continue
            started = datetime.datetime.fromisoformat(record["started_at"])
            if started.tzinfo is None or record.get("status") not in {
                "running", "passed", "failed", "interrupted", "stale"
            }:
                continue
        except (OSError, ValueError, TypeError, KeyError):
            continue
        if (record.get("source_digest") == source["source_digest"]
                and record.get("go_version") == toolchain
                and record.get("environment_digest") == verification_environment()
                and record.get("profile") == "full-race-v1"):
            matches.append((started, path, record))
    if matches:
        _, path, record = max(matches, key=lambda match: match[0])
        return {"status": record["status"], "record": str(path), "finished_at": record.get("finished_at")}
    return {"status": "unverified", "reason": "no matching source/toolchain/profile record"}
