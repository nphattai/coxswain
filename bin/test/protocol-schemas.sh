#!/bin/bash
# Validate every protocol spec's embedded example against its JSON Schema (docs/protocol/schema/<name>.v1.json).
# No jsonschema module or check-jsonschema is installed on this fleet, so this uses a MINIMAL draft-2020-12 validator
# (python3 stdlib only) covering the keywords these five schemas use: type, required, properties, additionalProperties
# (bool), enum, const, items, and local "$ref" (#/$defs/...). It is not a full JSON Schema implementation - it is a
# contract check that each documented example matches its schema. Run: /bin/bash bin/test/protocol-schemas.sh
set -eu
here="$(cd "$(dirname "$0")" && pwd)"; proto="$(cd "$here/../../docs/protocol" && pwd)"
python3 - "$proto" <<'PY'
import json, re, sys, pathlib
proto = pathlib.Path(sys.argv[1])
names = ["event", "checkpoint", "inbox", "wake", "fleet"]

def deref(schema, root):
    if isinstance(schema, dict) and "$ref" in schema:
        ref = schema["$ref"]
        node = root
        for part in ref.lstrip("#/").split("/"):
            node = node[part]
        return node
    return schema

def typ_ok(value, t):
    if t == "object":  return isinstance(value, dict)
    if t == "array":   return isinstance(value, list)
    if t == "string":  return isinstance(value, str)
    if t == "integer": return isinstance(value, int) and not isinstance(value, bool)
    if t == "number":  return isinstance(value, (int, float)) and not isinstance(value, bool)
    if t == "boolean": return isinstance(value, bool)
    if t == "null":    return value is None
    return True

def matches(value, schema, root):
    sub = []
    validate(value, schema, root, "", sub)
    return not sub

def validate(value, schema, root, path, errs):
    schema = deref(schema, root)
    for sub in schema.get("allOf", []):
        validate(value, sub, root, path, errs)
    if "if" in schema:
        if matches(value, schema["if"], root):
            if "then" in schema:
                validate(value, deref(schema["then"], root), root, path, errs)
        elif "else" in schema:
            validate(value, deref(schema["else"], root), root, path, errs)
    if "const" in schema and value != schema["const"]:
        errs.append(f"{path}: expected const {schema['const']!r}, got {value!r}")
    if "enum" in schema and value not in schema["enum"]:
        errs.append(f"{path}: {value!r} not in enum {schema['enum']}")
    t = schema.get("type")
    if t:
        types = t if isinstance(t, list) else [t]
        if not any(typ_ok(value, x) for x in types):
            errs.append(f"{path}: expected type {t}, got {type(value).__name__}")
            return
    if isinstance(value, dict):
        props = schema.get("properties", {})
        for req in schema.get("required", []):
            if req not in value:
                errs.append(f"{path}: missing required property '{req}'")
        if schema.get("additionalProperties") is False:
            for k in value:
                if k not in props:
                    errs.append(f"{path}: additional property '{k}' not allowed")
        for k, v in value.items():
            if k in props:
                validate(v, props[k], root, f"{path}.{k}", errs)
    if isinstance(value, list) and "items" in schema:
        for i, item in enumerate(value):
            validate(item, schema["items"], root, f"{path}[{i}]", errs)

fail = 0
for name in names:
    schema = json.loads((proto / "schema" / f"{name}.v1.json").read_text())
    md = (proto / f"{name}.v1.md").read_text()
    m = re.search(r"```json\n(.*?)\n```", md, re.S)
    if not m:
        print(f"FAIL {name}: no ```json example in {name}.v1.md"); fail = 1; continue
    try:
        example = json.loads(m.group(1))
    except json.JSONDecodeError as e:
        print(f"FAIL {name}: example is not valid JSON: {e}"); fail = 1; continue
    errs = []
    validate(example, schema, schema, name, errs)
    if errs:
        print(f"FAIL {name}:"); [print("  " + e) for e in errs]; fail = 1
    else:
        print(f"  ok {name}.v1 example validates against {name}.v1.json")

# Negative check: additionalProperties:true must not weaken `required`. Drop a required key from the event example
# and confirm the validator still flags it (an unknown top-level key is now tolerated, a missing required one is not).
schema = json.loads((proto / "schema" / "event.v1.json").read_text())
md = (proto / "event.v1.md").read_text()
bad = json.loads(re.search(r"```json\n(.*?)\n```", md, re.S).group(1))
bad.pop("attempt"); bad["unknown_future_field"] = "ignored"
errs = []
validate(bad, schema, schema, "neg", errs)
if any("missing required property 'attempt'" in e for e in errs):
    print("  ok negative: missing required 'attempt' is caught; unknown top-level field tolerated")
else:
    print(f"FAIL negative: missing required not caught after additionalProperties:true (errs={errs})"); fail = 1

# Negative check: a pending_external transition without evidence.intended_to must be flagged by the if/then rule, so
# Reconcile always has a target after a crash between the two appends (event.v1, M2).
pend = json.loads(re.search(r"```json\n(.*?)\n```", md, re.S).group(1))
pend["to"] = "pending_external"; pend["external_confirmed"] = False; pend["evidence"] = {"dispatch": "ctx_1"}
errs = []
validate(pend, schema, schema, "pend", errs)
if any("intended_to" in e for e in errs):
    print("  ok negative: pending_external without evidence.intended_to is caught")
else:
    print(f"FAIL negative: pending_external without intended_to not caught (errs={errs})"); fail = 1

sys.exit(fail)
PY
echo "ok: protocol-schemas (5 examples validate against their JSON Schemas)"
