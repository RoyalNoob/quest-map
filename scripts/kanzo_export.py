#!/usr/bin/env python3
"""Export tasks from a running Kanzo board into quest-map's on-disk layout (spec §3.1, §3.2).

For each exported Kanzo project it writes

    <board>/projects/<slug>/project.toml
    <board>/projects/<slug>/tasks/<KEY>-<n>.md      (or archive/ for done tasks with --archive-done)
    <board>/projects/<slug>/attachments/<KEY>-<n>/  (images attached in Kanzo)

Kanzo task numbers are kept, so `quest-map#6` becomes `QUESTMAP-6`; `<slug>#<n>` references in text are
rewritten the same way. Kanzo comments become log lines, waits-on links become `blocked_by`, tags become
labels and the "human" flag becomes `doer: person`.

The default target is a new folder, ./kanzo-export. Point --board at a real board (e.g. ~/.quest-map) to
write straight into it; existing project folders are never overwritten. The running quest-map server's
file watcher picks new files up; otherwise run `quest-map reindex`.

Only the Python standard library is used. Examples:

    python scripts/kanzo_export.py --dry-run
    python scripts/kanzo_export.py --project quest-map --key QM --board ~/.quest-map
"""

from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import re
import sys
import tomllib
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

LOG_MARKER = "<!-- quest-map:log -->"
STATUSES = {"backlog", "todo", "doing", "review", "done"}
SLUG_RE = re.compile(r"^[a-z0-9][a-z0-9-]*$")
KEY_RE = re.compile(r"^[A-Z][A-Z0-9]*$")
# Plain YAML scalars that need no quoting; anything else is written as a JSON string (valid YAML).
PLAIN_RE = re.compile(r"^[A-Za-z0-9_./][A-Za-z0-9_./ ()+=~-]*$")
YAML_WORDS = {"null", "true", "false", "yes", "no", "on", "off", "y", "n", "~"}


# ---------------------------------------------------------------------------------------------- Kanzo


class Kanzo:
    def __init__(self, base: str):
        self.base = base.rstrip("/")

    def _get(self, path: str) -> bytes:
        try:
            with urllib.request.urlopen(self.base + path, timeout=30) as r:
                return r.read()
        except urllib.error.URLError as e:
            sys.exit(f"kanzo: GET {self.base}{path}: {e}")

    def json(self, path: str):
        return json.loads(self._get(path).decode("utf-8"))

    def board(self) -> dict:
        return self.json("/api/board")

    def task(self, task_id: int) -> dict:
        return self.json(f"/api/tasks/{task_id}")

    def image(self, file: str) -> bytes:
        return self._get("/images/" + urllib.parse.quote(file))


# ------------------------------------------------------------------------------------------ formatting


def yaml_scalar(v) -> str:
    if v is None or v == "":
        return "null"
    s = str(v)
    if PLAIN_RE.match(s) and s.lower() not in YAML_WORDS and not s.endswith(" ") and not _numeric(s):
        return s
    return json.dumps(s, ensure_ascii=False)


def _numeric(s: str) -> bool:
    try:
        float(s)
        return True
    except ValueError:
        return False


def yaml_list(items) -> str:
    out = []
    for i in items:
        s = yaml_scalar(i)
        if "," in s and not s.startswith('"'):
            s = json.dumps(str(i), ensure_ascii=False)
        out.append(s)
    return "[" + ", ".join(out) + "]"


def toml_str(s: str) -> str:
    return json.dumps(s, ensure_ascii=False)  # TOML basic strings use the same escapes


def parse_time(s: str | None) -> dt.datetime | None:
    if not s:
        return None
    return dt.datetime.fromisoformat(s.replace("Z", "+00:00")).astimezone(dt.timezone.utc)


def rfc3339(t: dt.datetime | None) -> str:
    return t.strftime("%Y-%m-%dT%H:%M:%SZ") if t else "null"


def load_tz(board: Path):
    """Time zone for log lines, from the board's board.toml (spec §3.2); UTC when missing."""
    name = "UTC"
    cfg = board / "board.toml"
    if cfg.is_file():
        name = tomllib.loads(cfg.read_text(encoding="utf-8")).get("timezone", "UTC") or "UTC"
    if name == "UTC":
        return dt.timezone.utc
    if name == "Local":
        return None  # datetime.astimezone(None) = local time
    try:
        from zoneinfo import ZoneInfo

        return ZoneInfo(name)
    except Exception:
        print(f"warning: time zone {name!r} not available here (pip install tzdata); log times use UTC",
              file=sys.stderr)
        return dt.timezone.utc


def log_line(t: dt.datetime | None, who: str, text: str, tz) -> str:
    stamp = (t or dt.datetime.now(dt.timezone.utc)).astimezone(tz).strftime("%Y-%m-%d %H:%M")
    who = re.sub(r"[\s:]+", "-", who.strip()) or "kanzo"
    lines = text.strip().splitlines() or [""]
    # Continuation lines are indented two spaces (internal/board/task.go parseLog); blank lines too, or the
    # parser drops them and the comment's paragraphs run together.
    rest = ["  " + ln.rstrip() for ln in lines[1:]]
    return "\n".join([f"- {stamp} {who}: {lines[0].rstrip()}"] + rest)


# ---------------------------------------------------------------------------------------------- export


def default_key(slug: str) -> str:
    key = re.sub(r"[^A-Z0-9]", "", slug.upper())
    if not key or not key[0].isalpha():
        key = "K" + key
    return key


def repo_link(repo: str) -> str:
    """Kanzo stores GitHub repos as owner/name; quest-map keys remotes as git:<host>/<path>."""
    if ":" in repo:
        return repo
    return "git:github.com/" + repo.strip("/").lower()


def existing_repos(board: Path) -> dict[str, str]:
    out = {}
    for cfg in (board / "projects").glob("*/project.toml"):
        try:
            for r in tomllib.loads(cfg.read_text(encoding="utf-8")).get("repos", []):
                out[r] = cfg.parent.name
        except (OSError, tomllib.TOMLDecodeError):
            pass
    return out


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("--kanzo", default=os.environ.get("KANZO_URL", "http://localhost:4242"),
                    help="Kanzo base URL (default $KANZO_URL or http://localhost:4242)")
    ap.add_argument("--board", default="kanzo-export",
                    help="board folder to write into (default ./kanzo-export; e.g. ~/.quest-map)")
    ap.add_argument("--project", action="append", default=[],
                    help="Kanzo project slug to export; repeat for several (default: all with tasks)")
    ap.add_argument("--key", help="quest-map task-id prefix (only with one --project; default from slug)")
    ap.add_argument("--slug", help="quest-map project folder name (only with one --project)")
    ap.add_argument("--archive-done", action="store_true", help="put done tasks in archive/ instead of tasks/")
    ap.add_argument("--no-repos", action="store_true", help="do not copy Kanzo's repo links")
    ap.add_argument("--no-images", action="store_true", help="do not download attached images")
    ap.add_argument("--dry-run", action="store_true", help="show what would be written, write nothing")
    args = ap.parse_args()

    if (args.key or args.slug) and len(args.project) != 1:
        ap.error("--key and --slug need exactly one --project")
    if args.key and not KEY_RE.match(args.key):
        ap.error(f"--key {args.key!r}: use uppercase letters and digits, starting with a letter")
    if args.slug and not SLUG_RE.match(args.slug):
        ap.error(f"--slug {args.slug!r}: use lowercase letters, digits and dashes")

    board_dir = Path(os.path.expanduser(args.board)).resolve()
    kz = Kanzo(args.kanzo)
    data = kz.board()
    tz = load_tz(board_dir)

    projects = {p["slug"]: p for p in data["projects"]}
    tasks_by_project: dict[str, list[dict]] = {}
    for t in data["tasks"]:
        tasks_by_project.setdefault(t["project"], []).append(t)

    wanted = args.project or [s for s in projects if tasks_by_project.get(s)]
    for s in wanted:
        if s not in projects:
            sys.exit(f"kanzo has no project {s!r}; it has: {', '.join(sorted(projects))}")

    # Plan every project first, so cross-project references can be rewritten.
    plan = {}
    for s in wanted:
        key = args.key or default_key(s)
        slug = args.slug or (s if SLUG_RE.match(s) else re.sub(r"[^a-z0-9-]+", "-", s.lower()).strip("-"))
        plan[s] = {"key": key, "slug": slug, "dir": board_dir / "projects" / slug}
        if plan[s]["dir"].exists():
            sys.exit(f"{plan[s]['dir']} already exists; pick another --slug or remove it first")
    keys = [p["key"] for p in plan.values()]
    if len(set(keys)) != len(keys):
        sys.exit(f"two projects would share a key: {keys}; export them one at a time with --key")

    taken = existing_repos(board_dir)

    def ref(project: str, num: int) -> str | None:
        p = plan.get(project)
        return f"{p['key']}-{num}" if p else None

    def rewrite(text: str) -> str:
        def sub(m):
            r = ref(m.group(1), int(m.group(2)))
            return r or m.group(0)

        return re.sub(r"\b([a-z0-9][a-z0-9-]*)#(\d+)\b", sub, text or "")

    totals = {"tasks": 0, "comments": 0, "images": 0}
    for s in wanted:
        p, kp = plan[s], projects[s]
        key, pdir = p["key"], p["dir"]
        ktasks = sorted(tasks_by_project.get(s, []), key=lambda t: t["id"])
        print(f"{s} -> {pdir} (key {key}, {len(ktasks)} tasks)")

        repos = [] if args.no_repos else [repo_link(r) for r in kp.get("repos") or []]
        for r in list(repos):
            if r in taken:
                print(f"  warning: repo {r} is already linked to project {taken[r]!r}; not linking it here "
                      f"(move it with repo_link if wanted)")
                repos.remove(r)

        next_id = max((t["id"] for t in ktasks), default=0) + 1
        cfg = [f"key = {toml_str(key)}", f"name = {toml_str(kp.get('name') or s)}", f"next_id = {next_id}"]
        if repos:
            cfg.append("repos = [" + ", ".join(toml_str(r) for r in repos) + "]")
        files: dict[Path, bytes] = {pdir / "project.toml": ("\n".join(cfg) + "\n").encode()}

        for t in ktasks:
            full = kz.task(t["id"])
            task, comments, images = full["task"], full.get("comments", []), full.get("images", [])
            tid = f"{key}-{task['id']}"
            status = task["status"] if task["status"] in STATUSES else "backlog"
            blocked = [r for w in full.get("waits_on", []) if (r := ref(w["project"], w["id"]))]
            missing = [f"{w['project']}#{w['id']}" for w in full.get("waits_on", []) if not ref(w["project"], w["id"])]

            # Images: task-level ones go into the body, comment ones next to their log line.
            links: dict[object, list[str]] = {}
            if not args.no_images:
                for img in images:
                    name = re.sub(r"[^A-Za-z0-9._-]", "_", Path(img["file"]).name)
                    files[pdir / "attachments" / tid / name] = b"" if args.dry_run else kz.image(img["file"])
                    links.setdefault(img.get("comment_id"), []).append(f"![{name}](../attachments/{tid}/{name})")
                    totals["images"] += 1

            body = rewrite(task.get("description") or "").strip()
            extra = links.get(None, [])
            if missing:
                extra.append("Waits on (not exported): " + ", ".join(missing))
            body = "\n\n".join(x for x in [body, "\n".join(extra)] if x)

            log = [log_line(parse_time(task.get("created_at")), "kanzo",
                            f"imported from Kanzo {s}#{task['id']}", tz)]
            for c in sorted(comments, key=lambda c: (c.get("created_at") or "", c["id"])):
                text = rewrite(c.get("body") or "")
                if links.get(c["id"]):
                    text = (text + "\n" + "\n".join(links[c["id"]])).strip()
                log.append(log_line(parse_time(c.get("created_at")), c.get("author") or "kanzo", text, tz))
                totals["comments"] += 1

            fm = [
                "---",
                f"id: {tid}",
                f"title: {yaml_scalar(task['title'])}",
                f"status: {status}",
                f"doer: {'person' if task.get('human') else 'agent'}",
                "claim: null",
                "priority: normal",
                "discipline: null",
                "milestone: null",
                f"labels: {yaml_list(task.get('tags') or [])}",
                "asset: null",
                "pipeline: null",
                "found_in: null",
                "fixed_in: null",
                f"blocked_by: {yaml_list(blocked)}",
                "moved_from: null",
                f"url: {yaml_scalar(task.get('url'))}",
                f"created: {rfc3339(parse_time(task.get('created_at')))}",
                "---",
            ]
            text = "\n".join(fm) + "\n\n" + (body + "\n\n" if body else "") + LOG_MARKER + "\n## Log\n" + "\n".join(log) + "\n"
            sub = "archive" if args.archive_done and status == "done" else "tasks"
            files[pdir / sub / f"{tid}.md"] = text.encode("utf-8")
            totals["tasks"] += 1
            print(f"  {sub}/{tid}.md  [{status}] {task['title'][:70]}")

        if args.dry_run:
            continue
        for path, content in files.items():
            path.parent.mkdir(parents=True, exist_ok=True)
            tmp = path.with_name(path.name + ".tmp")
            tmp.write_bytes(content)
            os.replace(tmp, path)

    verb = "would write" if args.dry_run else "wrote"
    print(f"{verb} {totals['tasks']} tasks, {totals['comments']} comments as log lines, "
          f"{totals['images']} images under {board_dir}")
    if not args.dry_run and not (board_dir / "board.toml").is_file():
        print("this folder is not a board yet: copy projects/<slug> into ~/.quest-map/projects/, "
              "or open it with `quest-map serve --board " + str(board_dir) + "`")
    return 0


if __name__ == "__main__":
    sys.exit(main())
