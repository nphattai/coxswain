#!/bin/bash
# Effectiveness metrics of an epic from data already on disk - read-only, nothing is written anywhere.
# Usage: bin/scorecard.sh <project>/epics/<slug> [--story <id>] [--json]
#   no --story : one row per dispatched story, the retro table (paste into docs/history/)
#   --story id : the 12-metric block for that story
#   --json     : {schema:"crewkit-scorecard.v2", epic, run, generated, stories:[...]}
# v2 (2026-09-07, design 5.8): CTX-AVG (average context per api call, k tokens) and USD (list-price equivalent) from
# bin/session-cost.py's parser and prices; VERIFY% = CI run time of the PR's checks over story wall (gh run list).
# Sources: <epic>/.run, <epic>/.watch.log (both "HH:MM ..." and "%F HH:MM ..." lines), <epic>/inbox/<id>/handled/*.msg,
#   the worker's Claude session jsonl (~/.claude/projects/<worktree-with-dashes>/), story + evidence reflog in the story
#   worktree, `gh pr view`, `orca orchestration task-list`. An absent source prints "-", never an error.
# Env: SCORECARD_OFFLINE=1 skips gh/orca (tests); SCORECARD_PROJECTS_DIR overrides ~/.claude/projects (tests).
# Targets macOS /bin/bash 3.2 + one python3 block (shape of bin/context.sh).
# shellcheck disable=SC2154  # file-level: epic_dir, slug, state are set by epic_paths in lib.sh
set -uo pipefail
. "$(dirname "$0")/lib.sh"
. "$(dirname "$0")/inbox-lib.sh"
epic=""; story=""; json=0
while [ $# -gt 0 ]; do
  case "$1" in
    --story) story="${2:?--story <id>}"; shift 2;;
    --json) json=1; shift;;
    -*) echo "unknown flag $1" >&2; exit 2;;
    *) epic="$1"; shift;;
  esac
done
[ -n "$epic" ] || { echo "usage: scorecard.sh <project>/epics/<slug> [--story <id>] [--json]" >&2; exit 2; }
epic_paths "$epic"
[ -f "$state" ] || { echo "$slug: no .run yet (nothing dispatched) - no metrics"; exit 0; }

# story -> worktree map (tab-separated); stories = every dispatch.<id> key in .run, or the one asked for
if [ -n "$story" ]; then ids="$story"; else ids="$(sed -n 's/^dispatch\.\([^=]*\)=.*/\1/p' "$state" | awk '!seen[$0]++')"; fi
map=""
for id in $ids; do
  wt="$(story_worktree "$epic_dir" "$id" 2>/dev/null || true)"; [ -d "${wt:-/nonexistent}" ] || wt=""
  map="$map$id	$wt
"
done
SCORECARD_MAP="$map" SCORECARD_BIN="$(cd "$(dirname "$0")" && pwd)" python3 - "$epic_dir" "$slug" "$json" "$story" <<'PY'
import sys, os, re, json, glob, subprocess, datetime, time
epic_dir, slug, want_json, one_story = sys.argv[1], sys.argv[2], sys.argv[3] == "1", sys.argv[4] != ""
OFFLINE = os.environ.get("SCORECARD_OFFLINE") == "1"
PROJECTS = os.environ.get("SCORECARD_PROJECTS_DIR") or os.path.expanduser("~/.claude/projects")
stories = [l.split("\t") for l in os.environ.get("SCORECARD_MAP", "").splitlines() if l.strip()]

def sh(cmd, cwd=None, timeout=30):
    """stdout of a command or None - a missing tool or a failure is an absent source, never an error."""
    try:
        r = subprocess.run(cmd, cwd=cwd, capture_output=True, text=True, timeout=timeout)
        return r.stdout if r.returncode == 0 else None
    except Exception:
        return None

def iso(ts): return int(datetime.datetime.fromisoformat(ts.replace("Z", "+00:00")).timestamp())
def dur(sec):
    if sec is None: return "-"
    sec = int(sec); h, m = divmod(sec // 60, 60)
    return f"{h}h{m:02d}m" if h else f"{m}m"

# --- .run: append-only key=value; first started, last of everything else
run = {}; started = {}
for line in open(os.path.join(epic_dir, ".run")):
    if "=" not in line: continue
    k, v = line.rstrip("\n").split("=", 1)
    if k.startswith("started.") and k not in started: started[k] = v
    run[k] = v
run_id = run.get("run", "-")
disp2story = {v: k[9:] for k, v in run.items() if k.startswith("dispatch.")}
disp2story.update({v: k[5:] for k, v in run.items() if k.startswith("term.")})

# --- .watch.log: "HH:MM type rest" or "YYYY-MM-DD HH:MM type rest"; HH:MM-only lines are dated by folding backwards
# from the file mtime (a decreasing clock while walking up the file = the previous day)
LINE = re.compile(r"^(?:(\d{4}-\d\d-\d\d) )?(\d\d):(\d\d) (\S+) ?(.*)$")
events = []
wl = os.path.join(epic_dir, ".watch.log")
if os.path.exists(wl):
    raw = [m for m in (LINE.match(l.rstrip("\n")) for l in open(wl, errors="replace")) if m]
    day = datetime.date.fromtimestamp(os.path.getmtime(wl)); prev = None; out = []
    for m in reversed(raw):
        d, hh, mm, typ, rest = m.groups(); hm = int(hh) * 60 + int(mm)
        if d: day = datetime.date.fromisoformat(d)
        elif prev is not None and hm > prev: day -= datetime.timedelta(days=1)
        prev = hm
        out.append((int(time.mktime(datetime.datetime.combine(day, datetime.time(int(hh), int(mm))).timetuple())), typ, rest))
    events = list(reversed(out))

def story_of(typ, rest):
    head = rest.split(" ", 1)[0]
    if typ in ("RUNAWAY", "STUCK", "ring"):
        if typ == "STUCK": head = rest.split(" ")[1] if rest.startswith("inbox ") else head
        return head.split("/")[0]
    return disp2story.get(head)

import importlib.util
_sc = None
def session_cost():
    global _sc
    if _sc is None:
        spec = importlib.util.spec_from_file_location("session_cost", os.path.join(os.environ.get("SCORECARD_BIN", ""), "session-cost.py"))
        try:
            m = importlib.util.module_from_spec(spec)
            argv = sys.argv; sys.argv = ["session-cost.py"]   # its main body runs at import and exits on usage; the functions above it are what we need
            try: spec.loader.exec_module(m)
            except SystemExit: pass
            finally: sys.argv = argv
            _sc = m if hasattr(m, "api_calls") else False
        except Exception: _sc = False
    return _sc or None

def cost_stats(files):
    """average context per api call (tokens) and USD via session-cost.py; None when the parser is unavailable"""
    sc = session_cost()
    if not sc: return None
    calls = []
    for f in files:
        try: calls += sc.api_calls(sc.parse(f))
        except Exception: pass
    if not calls: return None
    ctx = [sum((c["usage"].get(k) or 0) for k in ("input_tokens", "cache_read_input_tokens", "cache_creation_input_tokens")) for c in calls]
    T = sc.tokens(calls)
    return {"calls": len(calls), "ctx_avg": int(sum(ctx) / len(ctx)), "usd": round(T["usd"], 1)}

def ci_seconds(wt, sid):
    """seconds of GitHub Actions runs on story/<id> (the PR gate), summed; None offline or without gh"""
    if OFFLINE or not wt: return None
    out = sh(["gh", "run", "list", "--branch", f"story/{sid}", "--limit", "50", "--json", "createdAt,updatedAt,status"], cwd=wt, timeout=60)
    if not out: return None
    try: runs = json.loads(out)
    except Exception: return None
    return sum(max(0, iso(r["updatedAt"]) - iso(r["createdAt"])) for r in runs if r.get("status") == "completed")

def jsonl_stats(wt):
    d = os.path.join(PROJECTS, wt.replace("/", "-")) if wt else None
    files = sorted(glob.glob(os.path.join(d, "*.jsonl"))) if d else []
    if not files: return None
    s = {"turns": 0, "out_tokens": 0, "compactions": [], "pr": None, "max_turn_s": 0, "first": None, "last": None, "cost": cost_stats(files)}
    for f in files:
        for line in open(f, errors="replace"):
            try: o = json.loads(line)
            except Exception: continue
            ts = o.get("timestamp")
            if ts:
                t = iso(ts); s["first"] = min(s["first"] or t, t); s["last"] = max(s["last"] or t, t)
            u = (o.get("message") or {}).get("usage") if o.get("type") == "assistant" else None
            if u: s["turns"] += 1; s["out_tokens"] += u.get("output_tokens", 0)
            if o.get("subtype") == "compact_boundary":
                cm = o.get("compactMetadata") or {}; s["compactions"].append((cm.get("trigger", "?"), cm.get("preTokens", 0)))
            if o.get("subtype") == "turn_duration": s["max_turn_s"] = max(s["max_turn_s"], o.get("durationMs", 0) // 1000)
            if o.get("type") == "pr-link" and o.get("prNumber") and (s["pr"] is None or iso(ts) < s["pr"]["at"]):
                s["pr"] = {"number": o["prNumber"], "repo": o.get("prRepository"), "at": iso(ts)}
    return s

def git_stats(wt, sid):
    if not wt: return None
    g = {"commits": [], "fixups": 0, "rebases": 0, "phase_trailers": set(), "found_by": {}, "evidence": []}
    ref = sh(["git", "reflog", "show", "--date=iso", "--format=%H%x09%gd%x09%gs", f"story/{sid}"], cwd=wt)
    if ref is None: return None
    for l in ref.splitlines():
        sha, gd, gs = (l.split("\t") + ["", ""])[:3]
        m = re.search(r"@\{(\d{4}-\d\d-\d\d \d\d:\d\d:\d\d) ([+-]\d{4})\}", gd)
        at = int(datetime.datetime.strptime(m.group(1) + m.group(2), "%Y-%m-%d %H:%M:%S%z").timestamp()) if m else None
        if gs.startswith("rebase (finish)"): g["rebases"] += 1
        if gs.startswith("commit"):
            subj = gs.split(": ", 1)[1] if ": " in gs else gs
            if subj.startswith("fixup!") or subj.startswith("squash!"): g["fixups"] += 1
            else: g["commits"].append((at, sha, subj))
            body = sh(["git", "show", "-s", "--format=%B", sha], cwd=wt) or ""
            for t in re.findall(r"^Phase:\s*(\d+)\s*/\s*(\d+)", body, re.M): g["phase_trailers"].add(t)
            for t in re.findall(r"^Found-by:\s*(\w+)", body, re.M): g["found_by"][t] = g["found_by"].get(t, 0) + 1
    g["commits"].sort(key=lambda c: c[0] or 0)
    for ref_name in (f"evidence/{sid}", f"origin/evidence/{sid}"):
        ev = sh(["git", "log", "--format=%at", ref_name], cwd=wt)
        if ev: g["evidence"] = sorted(int(x) for x in ev.split()); break
    return g

def gh_stats(pr, wt):
    if OFFLINE or not pr: return None
    cmd = ["gh", "pr", "view", str(pr["number"]), "--json", "number,createdAt,mergedAt,isDraft,reviews,additions,deletions,changedFiles,commits"]
    if pr.get("repo"): cmd += ["--repo", pr["repo"]]
    out = sh(cmd, cwd=wt or None, timeout=60)
    if not out: return None
    p = json.loads(out)
    return {"number": p["number"], "created": iso(p["createdAt"]), "merged": iso(p["mergedAt"]) if p.get("mergedAt") else None,
            "draft": p["isDraft"], "reviews": len(p.get("reviews") or []), "additions": p["additions"], "deletions": p["deletions"],
            "files": p["changedFiles"], "commits": len(p.get("commits") or [])}

tasks = {}
if not OFFLINE and run_id != "-":
    out = sh(["orca", "orchestration", "task-list", "--run", run_id, "--json"], timeout=60)
    if out:
        try:
            for t in json.loads(out)["result"]["tasks"]:
                res = t.get("result"); outcome = None
                if isinstance(res, str):
                    try: outcome = json.loads(res).get("outcome")
                    except Exception: pass
                tasks[t["id"]] = {"status": t.get("status"), "outcome": outcome}
        except Exception: pass

now = int(time.time()); rows = []
for sid, wt in stories:
    ev = [(t, typ, rest) for t, typ, rest in events if story_of(typ, rest) == sid]
    qs = [(t, rest) for t, typ, rest in ev if typ == "question"]
    ready_qs = [t for t, rest in qs if re.search(r"ready for review|PR .*ready", rest, re.I)]
    phases_seen = [int(x) for t, typ, rest in ev if typ in ("status", "question") for x in re.findall(r"\bphase (\d+)", rest, re.I)]
    hb_changes = sum(1 for t, typ, rest in ev if typ == "heartbeat")
    rings = [rest.split("-> ", 1)[1].split(" ")[0] for t, typ, rest in ev if typ == "ring" and "-> " in rest]
    inbox = os.path.join(epic_dir, "inbox", sid)
    steers = fyi = 0
    for rec in glob.glob(os.path.join(inbox, "handled", "*.msg")):
        head = open(rec, errors="replace").read(400)
        if "urgency=fyi" in head: fyi += 1
        else: steers += 1
    pending = len(glob.glob(os.path.join(inbox, "*.msg")))
    js = jsonl_stats(wt); gs = git_stats(wt, sid)
    pr = js["pr"] if js else None
    if not pr and wt and not OFFLINE:   # PR opened outside the session (no pr-link record): ask GitHub by branch
        out = sh(["gh", "pr", "list", "--head", f"story/{sid}", "--state", "all", "--json", "number", "--limit", "1"], cwd=wt, timeout=60)
        try: pr = {"number": json.loads(out)[0]["number"], "repo": None} if out else None
        except Exception: pr = None
    gh = gh_stats(pr, wt)
    t_start = int(started.get(f"started.{sid}", 0)) or None
    t_done = int(run.get(f"done.{sid}", 0)) or None
    t_end = t_done or (gh and gh["merged"]) or None
    wall = (t_end - t_start) if (t_start and t_end) else ((now - t_start) if t_start else None)
    plan_gate = qs[0][0] if qs else None
    t_ready = (ready_qs[-1] if ready_qs else None) or (gh and gh["merged"]) or None
    if gs and gs["phase_trailers"]: phases = len({n for n, m in gs["phase_trailers"]})
    elif phases_seen: phases = max(phases_seen)
    elif hb_changes: phases = hb_changes
    else: phases = None
    lines = (gh["additions"] + gh["deletions"]) if gh else None
    tok_per_line = (js["out_tokens"] / lines) if (js and lines) else None
    phase_times = []
    if gs and len(gs["commits"]) > 1:
        cs = [c for c in gs["commits"] if c[0]]
        phase_times = [cs[i + 1][0] - cs[i][0] for i in range(len(cs) - 1)]
    recaptures = len([t for t in (gs["evidence"] if gs else []) if t_ready and t > t_ready])
    task = tasks.get(run.get(f"task.{sid}", ""), {})
    ci = ci_seconds(wt, sid)
    cost = (js or {}).get("cost")
    row = {
        "ctx_avg": cost["ctx_avg"] if cost else None, "usd": cost["usd"] if cost else None, "calls": cost["calls"] if cost else None,
        "ci_s": ci, "verify_pct": round(100 * ci / wall) if (ci is not None and wall) else None,
        "id": sid, "worktree": wt or None, "dispatch": run.get(f"dispatch.{sid}"),
        "started": t_start, "done": t_done, "wall_s": wall, "open": t_end is None,
        "plan_gate": plan_gate, "ready": t_ready, "plan_to_ready_s": (t_ready - plan_gate) if (plan_gate and t_ready) else None,
        "dead_s": (plan_gate - t_start) if (plan_gate and t_start) else None,
        "phases": phases, "phase_source": "trailer" if (gs and gs["phase_trailers"]) else ("subject" if phases_seen else ("heartbeat" if hb_changes else None)),
        "phase_times_s": phase_times,
        "steers": steers, "fyi": fyi, "pending": pending, "questions": len(qs),
        "runaway": sum(1 for t, typ, r in ev if typ == "RUNAWAY"), "stuck": sum(1 for t, typ, r in ev if typ == "STUCK"),
        "rings_rang": rings.count("rang"), "rings_busy": sum(1 for r in rings if r.startswith("skipped")),
        "compactions": [{"trigger": a, "pre_tokens": b} for a, b in js["compactions"]] if js else None,
        "turns": js["turns"] if js else None, "out_tokens": js["out_tokens"] if js else None,
        "max_turn_s": js["max_turn_s"] if js else None,
        "commits": len(gs["commits"]) if gs else None, "fixups": gs["fixups"] if gs else None, "rebases": gs["rebases"] if gs else None,
        "found_by": gs["found_by"] if gs else None,
        "evidence_commits": len(gs["evidence"]) if gs else None, "evidence_recaptures": recaptures if gs else None,
        "pr": {"number": gh["number"], "draft": gh["draft"], "merged": gh["merged"], "reviews": gh["reviews"], "additions": gh["additions"],
               "deletions": gh["deletions"], "files": gh["files"], "commits": gh["commits"]} if gh else ({"number": pr["number"]} if pr else None),
        "tok_per_line": round(tok_per_line, 1) if tok_per_line else None,
        "task": task or None,
    }
    rows.append(row)

if want_json:
    print(json.dumps({"schema": "crewkit-scorecard.v2", "epic": slug, "run": run_id,
                      "generated": datetime.datetime.now().astimezone().isoformat(timespec="seconds"), "stories": rows}, indent=1))
    sys.exit(0)

def n(x): return "-" if x is None else str(x)
def prcell(p):
    if not p: return "-"
    s = f"#{p['number']}"
    if "merged" in p: s += " merged" if p["merged"] else (" draft" if p["draft"] else " open")
    return s
def cmp(c):
    if c is None: return "-"
    if not c: return "0"
    return f"{len(c)}({''.join(x['trigger'][0] for x in c)})"

if one_story and rows:
    r = rows[0]
    print(f"{r['id']}  run {run_id}  dispatch {n(r['dispatch'])}  worktree {n(r['worktree'])}")
    print(f" 1 wall              {dur(r['wall_s'])}{' (open)' if r['open'] else ''}   dead time (dispatch -> plan gate) {dur(r['dead_s'])}")
    print(f" 2 plan -> ready     {dur(r['plan_to_ready_s'])}   questions {r['questions']}")
    print(f" 3 phases            {n(r['phases'])} ({n(r['phase_source'])})   commits {n(r['commits'])}")
    print(f" 4 time per phase    {' '.join(dur(t) for t in r['phase_times_s']) or '-'}")
    print(f" 5 steers            {r['steers']} steer / {r['fyi']} fyi / {r['pending']} pending   rings rang {r['rings_rang']} busy {r['rings_busy']}")
    print(f" 6 interrupts        RUNAWAY {r['runaway']}  STUCK {r['stuck']}   longest turn {dur(r['max_turn_s'])}")
    print(f" 7 compactions       {cmp(r['compactions'])} {' '.join(str(c['pre_tokens'] // 1000) + 'k' for c in (r['compactions'] or []))}")
    print(f" 8 tokens            turns {n(r['turns'])}  output {n(r['out_tokens'])}  per changed line {n(r['tok_per_line'])}  calls {n(r['calls'])}  ctx avg {n(r['ctx_avg'] and r['ctx_avg'] // 1000)}k  usd {n(r['usd'])}  verify {n(r['verify_pct'])}% (ci {dur(r['ci_s'])})")
    print(f" 9 review rounds     fixups {n(r['fixups'])}  rebases {n(r['rebases'])}  reviews {n(r['pr'] and r['pr'].get('reviews'))}")
    print(f"10 evidence          commits {n(r['evidence_commits'])}  recaptures after ready {n(r['evidence_recaptures'])}")
    print(f"11 found-by          {n(r['found_by'])}")
    print(f"12 outcome           PR {prcell(r['pr'])}  task {n(r['task'] and r['task'].get('status'))}/{n(r['task'] and r['task'].get('outcome'))}  done {n(r['done'] and 'yes')}")
    sys.exit(0)

hdr = ("STORY", "WALL", "PLAN->READY", "PHASES", "CTX-AVG", "USD", "VERIFY%", "STEERS", "INT", "CMPCT", "FIXUP", "REBASE", "REVIEWS", "PR")
table = [hdr]
for r in rows:
    table.append((r["id"].replace(slug + "-", ""), dur(r["wall_s"]) + ("+" if r["open"] else ""), dur(r["plan_to_ready_s"]), n(r["phases"]),
                  (str(r["ctx_avg"] // 1000) + "k") if r["ctx_avg"] else "-", n(r["usd"]), n(r["verify_pct"]),
                  str(r["steers"]), str(r["runaway"]), cmp(r["compactions"]), n(r["fixups"]), n(r["rebases"]),
                  n(r["pr"] and r["pr"].get("reviews")), prcell(r["pr"])))
w = [max(len(row[i]) for row in table) for i in range(len(hdr))]
for row in table: print("  ".join(c.ljust(w[i]) for i, c in enumerate(row)).rstrip())
PY
