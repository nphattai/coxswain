#!/usr/bin/env python3
"""Read-only cost/time analysis of one epic's session logs (story ids are <epic slug>-<suffix>).
Prints aggregate tables only; never prints raw message text beyond a sanitized 60-char subject."""
import json, os, re, sys, glob, statistics, collections
from datetime import datetime, timedelta, timezone

# Usage: bin/session-cost.py <trace.json> <scorecard.json> <epic_dir>   (all produced by bin/scorecard.sh and the trace snapshot)
if __name__ == '__main__' and len(sys.argv) != 4:
    sys.exit('usage: session-cost.py <trace.json> <scorecard.json> <epic_dir>')
TRACE, SCORE, EPIC = (sys.argv[1:4] + [None, None, None])[:3]   # None when imported by bin/scorecard.sh for parse/api_calls/tokens/price
EPIC = EPIC.rstrip('/') if EPIC else None
PREFIX = os.path.basename(EPIC) + '-' if EPIC else ''   # story id prefix, e.g. example-app-mobile-; '' when imported by scorecard.sh
WATCH = f'{EPIC}/.watch.log'
INBOX = f'{EPIC}/inbox'
TZ = timezone(timedelta(hours=7))
# Opus 5 list prices per MTok (claude-api skill, cached 2026-06-24): in 5, out 25, cache read 0.1x, write 5m 1.25x, 1h 2x
PRICE = dict(inp=5.0, out=25.0, cr=0.5, c5=6.25, c1=10.0)  # opus-5 (default)
PRICES = {'claude-opus': PRICE, 'claude-fable-5-1': dict(inp=10.0, out=50.0, cr=0.25, c5=12.5, c1=20.0),
          'claude-fable-5': dict(inp=10.0, out=50.0, cr=1.0, c5=12.5, c1=20.0), 'claude-haiku': dict(inp=1.0, out=5.0, cr=0.1, c5=1.25, c1=2.0)}
def price(model):
    for pfx in ('claude-fable-5-1', 'claude-fable-5', 'claude-opus', 'claude-haiku'):
        if (model or '').startswith(pfx): return PRICES[pfx]
    return PRICE
IMG_TOK = 1500

def ts(s):
    return datetime.fromisoformat(s.replace('Z', '+00:00')).astimezone(TZ)

def short(story):
    return story.replace(PREFIX, '') if PREFIX else story

def sanitize(t, n=60):
    t = re.sub(r'\S+://\S+', '<url>', t)
    t = re.sub(r'\d{6,}', '#', t)
    t = re.sub(r'\s+', ' ', t).replace('\u2014', '-').strip()
    return t[:n]

PATH_RE = re.compile(r'(?:^|\s|["\'])((?:/|~/|\./)?[\w.@+-]+(?:/[\w.@+-]+)+\.\w{1,6})')
def bash_read_paths(cmd):
    out = set()
    for seg in re.split(r'[|;&]|\n', cmd):
        if re.match(r'\s*(cat|sed|head|tail|less|bat)\b', seg):
            out.update(PATH_RE.findall(seg))
    return out

def parse(file, t0=None, t1=None):
    """Compact per-line records. t0/t1 (aware datetimes) slice a shared session."""
    recs = []
    with open(file) as fh:
        for line in fh:
            try:
                d = json.loads(line)
            except Exception:
                continue
            if 'timestamp' not in d:
                continue
            t = ts(d['timestamp'])
            if (t0 and t < t0) or (t1 and t >= t1):
                continue
            r = {'type': d['type'], 't': t}
            typ = d['type']
            if typ == 'assistant':
                m = d['message']
                r['rid'] = d.get('requestId')
                r['usage'] = m.get('usage') or {}
                r['model'] = m.get('model')
                tools, text = [], ''
                for c in m.get('content', []):
                    if c.get('type') == 'tool_use':
                        inp = c.get('input') or {}
                        tools.append((c['name'], inp.get('file_path') or inp.get('command') or inp.get('pattern') or inp.get('skill') or ''))
                    elif c.get('type') == 'text':
                        text += c.get('text', '')
                r['tools'] = tools
                r['text'] = text[:200]
                r['mentions'] = collections.Counter(re.findall(re.escape(PREFIX) + r'[a-z-]+', line)) if PREFIX else collections.Counter()
                r['msgrefs'] = set(re.findall(r'(\d{3})\.msg', line))
                r['evidence'] = any(n == 'Bash' and ('capture.sh' in a or 'maestro' in a.lower()) for n, a in tools)
                r['png'] = any(n == 'Read' and a.endswith('.png') for n, a in tools)
            elif typ == 'user':
                m = d['message']
                c = m.get('content')
                r['meta'] = bool(d.get('isMeta'))
                r['msgrefs'] = set(re.findall(r'(\d{3})\.msg', line))
                r['img'] = 0
                if isinstance(c, str):
                    r['human'] = c[:120]
                else:
                    trs = [x for x in c if x.get('type') == 'tool_result']
                    if trs:
                        r['tool_result'] = True
                        for x in trs:
                            cc = x.get('content')
                            if isinstance(cc, list):
                                r['img'] += sum(1 for y in cc if y.get('type') == 'image')
                    else:
                        r['human'] = ' '.join(x.get('text', '') for x in c if x.get('type') == 'text')[:120]
            elif typ == 'system':
                r['subtype'] = d.get('subtype')
                r['durationMs'] = d.get('durationMs')
                r['compact'] = d.get('compactMetadata')
            recs.append(r)
    return recs

def api_calls(recs):
    """Dedupe assistant lines per requestId -> list of calls with usage, tools, text."""
    calls, byrid = [], {}
    for r in recs:
        if r['type'] != 'assistant':
            continue
        c = byrid.get(r['rid'])
        if c is None:
            c = byrid[r['rid']] = {'t': r['t'], 'usage': r['usage'], 'tools': [], 'text': '', 'mentions': collections.Counter(), 'model': r.get('model')}
            calls.append(c)
        u = r['usage']
        if (u.get('output_tokens') or 0) >= (c['usage'].get('output_tokens') or 0):
            c['usage'] = u
        c['tools'] += r['tools']
        c['text'] += r['text']
        c['mentions'] += r['mentions']
    return calls

def tokens(calls):
    T = collections.Counter()
    for c in calls:
        u = c['usage']; P = price(c.get('model'))
        T['usd'] += ((u.get('input_tokens') or 0) * P['inp'] + (u.get('output_tokens') or 0) * P['out'] + (u.get('cache_read_input_tokens') or 0) * P['cr']
                     + ((u.get('cache_creation') or {}).get('ephemeral_5m_input_tokens', 0)) * P['c5']
                     + ((u.get('cache_creation') or {}).get('ephemeral_1h_input_tokens', (u.get('cache_creation_input_tokens') or 0) if not u.get('cache_creation') else 0)) * P['c1']) / 1e6
        T['model:' + re.sub(r'-\d{8}$', '', c.get('model') or '?')] += 1
        T['out'] += u.get('output_tokens') or 0
        T['inp'] += u.get('input_tokens') or 0
        cc = u.get('cache_creation_input_tokens') or 0
        T['cc'] += cc
        T['cr'] += u.get('cache_read_input_tokens') or 0
        br = u.get('cache_creation') or {}
        T['c1'] += br.get('ephemeral_1h_input_tokens', cc if not br else 0)
        T['c5'] += br.get('ephemeral_5m_input_tokens', 0)
    T['ctx'] = T['inp'] + T['cc'] + T['cr']
    T['hit'] = T['cr'] / T['ctx'] if T['ctx'] else 0
    T['calls'] = len(calls)
    return T

def pct(a, b):
    return f'{100 * a / b:.0f}%' if b else '-'

def k(n):
    return f'{n / 1000:.0f}k' if abs(n) >= 1000 else str(n)

def med_p90(xs):
    if not xs:
        return '-', '-'
    xs = sorted(xs)
    return f'{statistics.median(xs):.0f}', f'{xs[min(len(xs) - 1, int(0.9 * (len(xs) - 1)))]:.0f}'

def table(header, rows):
    print('| ' + ' | '.join(header) + ' |')
    print('|' + '|'.join('---' for _ in header) + '|')
    for r in rows:
        print('| ' + ' | '.join(str(x) for x in r) + ' |')
    print()

# ---------------------------------------------------------------- load
if __name__ != '__main__':
    raise SystemExit(0)   # imported: the functions above are the API, the report below needs the three files
trace = json.load(open(TRACE))
score = {s['id']: s for s in json.load(open(SCORE))['stories']}
stories = {s['story']: s for s in trace['stories']}
ctx2story = {s['dispatch']: s['story'] for s in trace['stories']}
term2story = {s['terminal']: s['story'] for s in trace['stories'] if s.get('terminal')}
order = sorted(stories, key=lambda s: int(stories[s]['started']))

def epoch(e):
    return datetime.fromtimestamp(int(e), TZ) if e else None

# shared session (design-scout + foundation): slice at foundation start
S = {}
for name in order:
    s = stories[name]
    f = s['sessions'][0]['file']
    sharers = [o for o in order if o != name and stories[o]['sessions'][0]['file'] == f and 'no Claude session' not in (stories[o].get('note') or '')]
    t0 = t1 = None
    if sharers:
        others = sorted(sharers + [name], key=lambda x: int(stories[x]['started']))
        i = others.index(name)
        t0 = epoch(stories[name]['started']) - timedelta(minutes=5) if i > 0 else None
        t1 = epoch(stories[others[i + 1]]['started']) - timedelta(minutes=5) if i + 1 < len(others) else None
    S[name] = [] if 'no Claude session' in (s.get('note') or '') else parse(f, t0, t1)
leader = parse(trace['leader_sessions'][0]['file'])

# watch.log
watch = []
date = datetime(2026, 9, 1, tzinfo=TZ).date()
prev = None
for line in open(WATCH):
    tok = line.split()
    if len(tok) < 3:
        continue
    if re.match(r'^\d{4}-\d\d-\d\d$', tok[0]):
        date = datetime.strptime(tok[0], '%Y-%m-%d').date(); tok = tok[1:]
    if not re.match(r'^\d\d:\d\d$', tok[0]):
        continue
    hh, mm = map(int, tok[0].split(':'))
    cur = hh * 60 + mm
    if prev is not None and cur < prev - 60:
        date = date + timedelta(days=1)
    prev = cur
    t = datetime(date.year, date.month, date.day, hh, mm, tzinfo=TZ)
    ident = tok[2] if len(tok) > 2 else ''
    story = ctx2story.get(ident) or term2story.get(ident) or (ident if ident in stories else None)
    watch.append({'t': t, 'type': tok[1], 'story': story})

# inbox records
inbox = collections.defaultdict(list)
for f in sorted(glob.glob(f'{INBOX}/*/handled/*.msg')):
    story = f.split('/')[-3]
    inbox[story].append({'n': os.path.basename(f)[:3], 't': datetime.fromtimestamp(os.path.getmtime(f), TZ)})

# ---------------------------------------------------------------- 1. tokens
print('## 1. Token totals (list prices per MTok - opus-5: in 5 / out 25 / cache read 0.5 / write 1h 10; fable-5: 10 / 50 / 1.0 / 20; fable-5.1: 10 / 50 / 0.25 / 20)\n')
rows, TOT = [], collections.Counter()
percall = {}
for name in order + ['LEADER']:
    calls = api_calls(leader if name == 'LEADER' else S[name])
    T = tokens(calls); percall[name] = (calls, T)
    for kk in ('out', 'inp', 'cc', 'cr', 'c1', 'c5', 'ctx', 'usd', 'calls'):
        TOT[kk] += T[kk]
    wall = (score.get(name, {}).get('wall_s') or 0) / 3600 if name != 'LEADER' else (calls[-1]['t'] - calls[0]['t']).total_seconds() / 3600 if calls else 0
    rows.append([short(name), T['calls'], k(T['out']), k(T['inp']), k(T['cc']), k(T['cr']), f"{T['hit']:.2f}", k(T['ctx'] / T['calls']) if T['calls'] else '-', f"${T['usd']:.0f}", f'{wall:.1f}h', f"${T['usd'] / wall:.0f}" if wall else '-'])
TOT['hit'] = TOT['cr'] / TOT['ctx'] if TOT['ctx'] else 0
rows.append(['TOTAL', TOT['calls'], k(TOT['out']), k(TOT['inp']), k(TOT['cc']), k(TOT['cr']), f"{TOT['hit']:.2f}", k(TOT['ctx'] / TOT['calls']), f"${TOT['usd']:.0f}", '', ''])
table(['story', 'api calls', 'output', 'input fresh', 'cache write', 'cache read', 'hit', 'avg ctx/call', 'est $', 'wall', '$/h'], rows)
print('Models (api calls): ' + '; '.join(short(n) + ': ' + ', '.join(f"{m[6:]}={v}" for m, v in sorted(percall[n][1].items()) if m.startswith('model:')) for n in order + ['LEADER'] if percall[n][0]) + '\n')
for cap in (200_000, 300_000):
    saved = sum(max(0, (c['usage'].get('cache_read_input_tokens') or 0) - cap) * price(c.get('model'))['cr'] for n in percall for c in percall[n][0]) / 1e6
    over = sum(1 for n in percall for c in percall[n][0] if (c['usage'].get('cache_read_input_tokens') or 0) > cap)
    print(f'If context were compacted at {cap // 1000}k: {over} calls exceed it; cache-read cost above the cap = ${saved:.0f} ({pct(saved, TOT["usd"])} of total).')
print()
w1h = sum(percall[n][1]['c1'] for n in percall); w5m = sum(percall[n][1]['c5'] for n in percall)
cost_split = collections.Counter()
for n in percall:
    for c in percall[n][0]:
        u = c['usage']; P = price(c.get('model')); br = u.get('cache_creation') or {}
        cost_split['out'] += (u.get('output_tokens') or 0) * P['out']; cost_split['inp'] += (u.get('input_tokens') or 0) * P['inp']; cost_split['cr'] += (u.get('cache_read_input_tokens') or 0) * P['cr']
        cost_split['cw'] += br.get('ephemeral_5m_input_tokens', 0) * P['c5'] + br.get('ephemeral_1h_input_tokens', (u.get('cache_creation_input_tokens') or 0) if not br else 0) * P['c1']
tc = sum(cost_split.values())
print(f"Cache writes: {pct(w1h, w1h + w5m)} at 1h TTL. Dollar split: output {pct(cost_split['out'], tc)}, cache write {pct(cost_split['cw'], tc)}, cache read {pct(cost_split['cr'], tc)}, fresh input {pct(cost_split['inp'], tc)}.\n")

# ---------------------------------------------------------------- 2. tools
print('## 2. Tool mix, turn shape, image results\n')
rows = []
for name in order + ['LEADER']:
    calls, T = percall[name]
    recs = leader if name == 'LEADER' else S[name]
    tc_ = collections.Counter(); tool_out = collections.Counter()
    tool_only = text_only = mixed = 0
    for c in calls:
        names = [n for n, _ in c['tools']]
        for n in names:
            tc_[n] += 1
        o = c['usage'].get('output_tokens') or 0
        if names:
            for n in set(names):
                tool_out[n] += o / len(set(names))
        if names and not c['text'].strip():
            tool_only += 1
        elif names:
            mixed += 1
        else:
            text_only += 1
    imgs = sum(r.get('img', 0) for r in recs if r['type'] == 'user')
    top = ' '.join(f'{n}:{tc_[n]}' for n, _ in tc_.most_common(6))
    topo = ' '.join(f'{n}:{k(tool_out[n])}' for n, _ in tool_out.most_common(4))
    rows.append([short(name), sum(tc_.values()), top, topo, pct(tool_only, len(calls)), pct(mixed, len(calls)), pct(text_only, len(calls)), imgs, k(imgs * IMG_TOK), pct(imgs * IMG_TOK, T['inp'] + T['cc'])])
table(['story', 'tool calls', 'by tool (count)', 'output tok by tool', 'tool-only', 'text+tool', 'text-only', 'png results', 'img tok est', 'img/new-input'], rows)
print(f'img tok est = png tool results x {IMG_TOK}; img/new-input = share of (fresh input + cache write), i.e. tokens entering context once.\n')

# ---------------------------------------------------------------- 3. time
print('## 3. Time structure\n')
rows = []
idle_detail = {}
for name in order + ['LEADER']:
    calls, T = percall[name]
    recs = leader if name == 'LEADER' else S[name]
    tl = [r for r in recs if r['type'] in ('user', 'assistant', 'system', 'pr-link')]
    if not tl:
        continue
    span = (tl[-1]['t'] - tl[0]['t']).total_seconds() / 3600
    gaps = []
    for a, b in zip(tl, tl[1:]):
        g = (b['t'] - a['t']).total_seconds() / 60
        if g > 10:
            gaps.append((a['t'], g))
    idle_detail[name] = gaps
    idle = sum(g for _, g in gaps)
    tds = [r for r in recs if r['type'] == 'system' and r.get('subtype') == 'turn_duration']
    tdur = sorted([r['durationMs'] / 60000 for r in tds], reverse=True)
    rows.append([short(name), f'{span:.1f}h', f'{len(calls) / span:.0f}' if span else '-', f'{len(calls) / (span - idle / 60):.0f}' if span - idle / 60 > 0 else '-', len(gaps), f'{idle:.0f}', pct(idle / 60, span), len(tds), ' '.join(f'{x:.0f}' for x in tdur[:5])])
table(['story', 'span', 'calls/h', 'calls/h active', 'idle gaps>10m', 'idle min', 'idle share', 'prompt turns', 'longest turns (min)'], rows)

print('Longest 5 prompt-turns per story (turn_duration) with sanitized subject of what the assistant was doing:\n')
rows = []
for name in order + ['LEADER']:
    recs = leader if name == 'LEADER' else S[name]
    tds = []
    for i, r in enumerate(recs):
        if r['type'] == 'system' and r.get('subtype') == 'turn_duration':
            subj = ''
            for j in range(i - 1, max(-1, i - 400), -1):
                if recs[j]['type'] == 'assistant' and recs[j]['text'].strip():
                    subj = sanitize(recs[j]['text']); break
            tds.append((r['durationMs'] / 60000, r['t'], subj))
    for dur, t, subj in sorted(tds, reverse=True)[:5 if name != 'LEADER' else 5]:
        rows.append([short(name), f'{dur:.0f}', t.strftime('%d %H:%M'), subj])
table(['story', 'min', 'ended', 'subject'], rows)

print('Idle gaps > 10 min (start time, minutes), per story:\n')
for name in order:
    gaps = idle_detail.get(name, [])
    if gaps:
        print(f"- {short(name)}: " + ', '.join(f"{t.strftime('%d %H:%M')}({g:.0f})" for t, g in gaps[:14]) + (' ...' if len(gaps) > 14 else ''))
print()

# ---------------------------------------------------------------- 4. leader latency
print('## 4. Leader latency\n')
rows, allq, allsteer = [], [], []
for name in order:
    qs = [w['t'] for w in watch if w['type'] == 'question' and w['story'] == name]
    recs = S[name]
    humans = [r['t'] for r in recs if r['type'] == 'user' and 'human' in r and not r.get('meta')]
    lat, unans = [], 0
    for q in qs:
        cands = [x['t'] for x in inbox[name] if x['t'] > q] + [t for t in humans if t > q]
        if cands:
            lat.append((min(cands) - q).total_seconds() / 60)
        else:
            unans += 1
    allq += lat
    # steer landing: inbox mtime -> first line referencing NNN.msg
    land, unseen = [], 0
    for x in inbox[name]:
        hit = next((r['t'] for r in recs if r['t'] > x['t'] and x['n'] in r.get('msgrefs', ())), None)
        if hit:
            land.append((hit - x['t']).total_seconds() / 60)
        else:
            unseen += 1
    allsteer += land
    m, p = med_p90(lat); m2, p2 = med_p90(land)
    rows.append([short(name), len(qs), unans, m, p, len(inbox[name]), unseen, m2, p2])
m, p = med_p90(allq); m2, p2 = med_p90(allsteer)
rows.append(['ALL', sum(r[1] for r in rows), sum(r[2] for r in rows), m, p, sum(r[5] for r in rows), sum(r[6] for r in rows), m2, p2])
table(['story', 'questions', 'no reply found', 'reply med min', 'reply p90', 'inbox records', 'never referenced', 'landing med min', 'landing p90'], rows)
print('reply = earliest of next inbox record mtime or next human user line in the worker jsonl after the watch.log question (minute resolution).\n')

# ---------------------------------------------------------------- 5. compaction
print('## 5. Compaction cost\n')
rows = []
for name in order + ['LEADER']:
    recs = leader if name == 'LEADER' else S[name]
    for i, r in enumerate(recs):
        if r['type'] != 'system' or r.get('subtype') != 'compact_boundary' or not r.get('compact'):
            continue
        cm = r['compact']
        before = set()
        for q in recs[:i]:
            if q['type'] == 'assistant':
                for n, arg in q['tools']:
                    if n == 'Read':
                        before.add(arg)
                    elif n == 'Bash':
                        before |= bash_read_paths(arg)
        after_reads = reread = 0
        rebuild_out = 0
        for q in recs[i + 1:]:
            if (q['t'] - r['t']).total_seconds() > 1800:
                break
            if q['type'] == 'assistant':
                rebuild_out += 0
                for n, arg in q['tools']:
                    ps = {arg} if n == 'Read' else bash_read_paths(arg) if n == 'Bash' else set()
                    if n == 'Read' or ps:
                        after_reads += 1
                        if ps & before:
                            reread += 1
        rows.append([short(name), r['t'].strftime('%d %H:%M'), cm.get('trigger'), k(cm.get('preTokens', 0)), k(cm.get('postTokens', 0)), f"{cm.get('durationMs', 0) / 1000:.0f}s", len(before), after_reads, reread])
table(['story', 'when', 'trigger', 'preTokens', 'postTokens', 'compact time', 'files read before', 'reads in 30m after', 'of which re-reads'], rows)

# ---------------------------------------------------------------- 6. leader
print('## 6. Leader session\n')
calls, T = percall['LEADER']
print(f"Total: {T['calls']} api calls, output {k(T['out'])}, fresh input {k(T['inp'])}, cache write {k(T['cc'])}, cache read {k(T['cr'])}, hit {T['hit']:.2f}, est ${T['usd']:.0f}.\n")
kinds = collections.Counter()
for r in leader:
    if r['type'] != 'user' or 'human' not in r:
        continue
    low = r['human'].lower()
    if r.get('meta'):
        kinds['meta (skill/image/caveat)'] += 1
    elif low.startswith('watch:') or '.watch.queue' in low or 'wake' in low:
        kinds['hook wakeup'] += 1
    elif low.startswith('<'):
        kinds['tagged: ' + re.match(r'<([\w-]+)', low).group(1) if re.match(r'<([\w-]+)', low) else 'tagged'] += 1
    elif low.startswith('[request interrupted'):
        kinds['captain interrupt'] += 1
    else:
        kinds['captain prompt'] += 1
table(['user line kind', 'count'], sorted(kinds.items(), key=lambda x: -x[1]))
tc_ = collections.Counter()
for c in calls:
    for n, arg in c['tools']:
        if n == 'Bash':
            m = re.search(r'bin/([\w-]+\.sh)', arg)
            tc_['Bash ' + (m.group(1) if m else 'other')] += 1
        elif n == 'Read':
            tc_['Read ' + ('png' if arg.endswith('.png') else 'other')] += 1
        else:
            tc_[n] += 1
table(['leader tool', 'calls'], tc_.most_common(18))
bo = collections.Counter()
for c in calls:
    for n, arg in c['tools']:
        if n == 'Bash' and not re.search(r'bin/[\w-]+\.sh', arg):
            first = re.sub(r'^\s*(cd\s+\S+\s*(&&|;)\s*)?', '', arg).split()
            bo[first[0] if first else '?'] += 1
table(['leader Bash other: first command', 'calls'], bo.most_common(12))

def cat(c):
    names = [(n, a) for n, a in c['tools']]
    if any(n == 'Bash' and 'send.sh' in a for n, a in names): return 'inbox write (send.sh)'
    if any(n == 'Bash' and 'audit-pr.sh' in a for n, a in names): return 'audit (audit-pr.sh)'
    if any(n == 'Bash' and 'status.sh' in a for n, a in names): return 'status (status.sh)'
    if any(n == 'Bash' and 'wake-drain.sh' in a for n, a in names): return 'wake drain'
    if any(n == 'Bash' and ('worker-tail' in a or 'context.sh' in a) for n, a in names): return 'worker tail/context'
    if any(n == 'Read' and a.endswith('.png') for n, a in names): return 'read frames (png)'
    if any(n == 'Bash' and 'dispatch.sh' in a for n, a in names): return 'dispatch'
    if names: return 'other tool'
    return 'text only (captain dialogue)'
co = collections.Counter(); cn = collections.Counter()
for c in calls:
    co[cat(c)] += c['usage'].get('output_tokens') or 0; cn[cat(c)] += 1
table(['leader output category', 'api calls', 'output tok', 'share'], [(a, cn[a], k(b), pct(b, T['out'])) for a, b in co.most_common()])
hours = collections.defaultdict(collections.Counter)
for c in calls:
    h = c['t'].strftime('%d %Hh'); hours[h]['calls'] += 1; hours[h]['out'] += c['usage'].get('output_tokens') or 0
    hours[h]['ctx'] += (c['usage'].get('input_tokens') or 0) + (c['usage'].get('cache_creation_input_tokens') or 0) + (c['usage'].get('cache_read_input_tokens') or 0)
hs = sorted(hours)
print('Leader tokens per hour of day (day hour: calls / output / context billed):\n')
print('| ' + ' | '.join(['hour'] + hs) + ' |'); print('|' + '---|' * (len(hs) + 1))
print('| calls | ' + ' | '.join(str(hours[h]['calls']) for h in hs) + ' |')
print('| out | ' + ' | '.join(k(hours[h]['out']) for h in hs) + ' |')
print('| ctx | ' + ' | '.join(k(hours[h]['ctx']) for h in hs) + ' |\n')
attr = collections.Counter(); last = 'none'
for c in calls:
    ms = sorted((m for m in c['mentions'] if m in stories), key=lambda m: (-c['mentions'][m], m))
    if ms:
        last = ms[0]
    attr[last] += c['usage'].get('output_tokens') or 0
table(['leader output attributed to story', 'output tok', 'share'], [(short(a), k(b), pct(b, T['out'])) for a, b in attr.most_common()])

# ---------------------------------------------------------------- 7. evidence
print('## 7. Evidence phase share\n')
rows = []
for name in order:
    recs = S[name]
    ev = [r['t'] for r in recs if r.get('evidence')]
    if not ev:
        rows.append([short(name), '-', '', '', '', '', '', '', '']); continue
    prs = [r['t'] for r in recs if r['type'] == 'pr-link']
    t0 = ev[0]; t1 = max([ev[-1]] + [p for p in prs if p > ev[0]][-1:])
    calls, T = percall[name]
    mins = (t1 - t0).total_seconds() / 60
    total_min = (recs[-1]['t'] - recs[0]['t']).total_seconds() / 60
    evcalls = [c for c in calls if any((n == 'Bash' and ('capture.sh' in a or 'maestro' in a.lower())) or (n == 'Read' and a.endswith('.png')) for n, a in c['tools'])]
    ncap = sum(1 for c in calls for n, a in c['tools'] if n == 'Bash' and ('capture.sh' in a or 'maestro' in a.lower()))
    Te = tokens(evcalls)
    rows.append([short(name), t0.strftime('%d %H:%M'), t1.strftime('%d %H:%M'), f'{mins:.0f}', pct(mins, total_min), ncap, len(evcalls), pct(Te['out'], T['out']), pct(Te['usd'], T['usd'])])
table(['story', 'first capture call', 'end (last capture or later pr-link)', 'window min', 'window share', 'capture/maestro calls', 'evidence api calls (capture+png read)', 'out share', '$ share'], rows)
print('window = first capture.sh/maestro tool call to the later of last such call and the pr-link after it; evidence api calls = calls that ran capture/maestro or Read a png.')
