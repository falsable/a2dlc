"""Reference reading of RFC-0001 v3.5 §6.4 (the normative receipt), written from the RFC text alone: the tree_hash of a
commit, the WaiverSHA and the signed approval text. It exists to prove the RFC is enough to reproduce the binary's bytes
(e2e/test_rfc_reference.py compares it with Go's output) and to compute the expected values of the conformance vectors
(e2e/make_conformance_vectors.py). It is not part of the conformance kit and no verifier needs it."""
import hashlib
import subprocess

# RFC §6.4.2: the gate outputs and the signatures. Each entry is an exact path or a directory (prefix at a "/").
EXCLUDED = [".a2dlc/verdict.json", ".a2dlc/verdict.sig", ".a2dlc/reports", ".a2dlc/approvals.json", ".a2dlc/authorizations.json", ".a2dlc/escalations.json", ".a2dlc/run"]

_SHORT = {'"': '\\"', "\\": "\\\\", "\n": "\\n", "\r": "\\r", "\t": "\\t", "\b": "\\b", "\f": "\\f"}


def _string(s):
    out = ['"']
    for ch in s:
        o = ord(ch)
        if ch in _SHORT:
            out.append(_SHORT[ch])
        elif o < 0x20 or ch in "<>&" or o in (0x2028, 0x2029):
            out.append("\\u%04x" % o)
        elif 0xD800 <= o <= 0xDFFF:
            out.append("�")  # a lone surrogate from a \uD800 escape decodes to U+FFFD
        else:
            out.append(ch)
    out.append('"')
    return "".join(out)


def waiver_bytes(target, decision, reason):
    """RFC §6.4.6: the bytes whose sha256 is the waiver_sha256."""
    return ('{"target":%s,"decision":%s,"reason":%s}' % (_string(target), _string(decision), _string(reason))).encode("utf-8")


def waiver_sha256(target, decision, reason):
    return hashlib.sha256(waiver_bytes(target, decision, reason)).hexdigest()


def approval_payload(target, waiver_sha, file_blob):
    """RFC §6.4.7: the text that is signed."""
    return ("a2dlc-approval/1\n%s\n%s\n%s\n" % (target, waiver_sha, file_blob)).encode("utf-8")


def _excluded(path):
    return any(path == e or path.startswith(e + "/") for e in EXCLUDED)


def _git(repo, *args):
    return subprocess.run(["git", "-C", repo, *args], check=True, capture_output=True).stdout


def object_format(repo):
    return _git(repo, "rev-parse", "--show-object-format").decode().strip()


def blob_id(content, algo="sha1"):
    """The git object id of a blob with this content."""
    return hashlib.new(algo, b"blob %d\0" % len(content) + content).hexdigest()


def tree_hash(repo, commit):
    """RFC §6.4.2: git tree id of `commit` after removing the excluded paths, as "git-tree:<hex>". Built by hand from the
    entries `git ls-tree` lists, so it does not lean on git's write-tree."""
    entries = []
    for rec in _git(repo, "ls-tree", "-r", "-z", "--full-tree", commit).split(b"\0"):
        if not rec:
            continue
        meta, path = rec.split(b"\t", 1)
        mode, _kind, oid = meta.decode().split(" ")
        entries.append((path.decode("utf-8", "surrogateescape"), mode, oid))
    return tree_hash_entries(entries, object_format(repo))


def tree_hash_entries(entries, algo="sha1"):
    """The same from (path, mode, object id in hex) entries, one per file, link or gitlink of the commit."""
    root = {}
    for path, mode, oid in entries:
        if _excluded(path):
            continue
        node = root
        parts = path.split("/")
        for p in parts[:-1]:
            node = node.setdefault(p, {})
        node[parts[-1]] = (mode, oid)

    def write(node):
        items = []
        for name, v in node.items():
            if isinstance(v, dict):
                items.append((name + "/", b"40000", write(v), name))
            else:
                items.append((name, v[0].lstrip("0").encode(), bytes.fromhex(v[1]), name))
        out = b""
        for _key, mode, oid, name in sorted(items, key=lambda e: e[0].encode("utf-8", "surrogateescape")):
            out += mode + b" " + name.encode("utf-8", "surrogateescape") + b"\0" + oid
        return hashlib.new(algo, b"tree %d\0" % len(out) + out).digest()

    return "git-tree:" + write(root).hex()


# ----- RFC-0007 Part B and RFC-0008 section 4: signed texts and the run ledger ------------------------------------------------


def authorization_payload(base_ref, intent_sha256, decision, expires_at, targets):
    """RFC-0007 Part B: the text of an authorization, targets sorted and without repeats."""
    return ("a2dlc-authorization/1\n%s\n%s\n%s\n%s\n" % (base_ref, intent_sha256, decision, expires_at) + "".join(t + "\n" for t in sorted(set(targets)))).encode("utf-8")


def plan_payload(plan_sha256, base_ref, expires_at):
    """RFC-0008 section 4: the text of a plan approval."""
    return ("a2dlc-plan/1\n%s\n%s\n%s\n" % (plan_sha256, base_ref, expires_at)).encode("utf-8")


def _float(x):
    """Shortest text that reads back as x, in the style of ECMAScript Number::toString (no exponent from 1e-6 to below 1e21)."""
    if x == int(x) and abs(x) < 1e21:
        return str(int(x))
    r = repr(float(x))
    if "e" not in r:
        return r
    mant, exp = r.split("e")
    e = int(exp)
    if -6 <= e < 21:
        digits = mant.replace(".", "").replace("-", "")
        sign = "-" if mant.startswith("-") else ""
        if e < 0:
            return sign + "0." + "0" * (-e - 1) + digits
        return sign + digits + "0" * (e + 1 - len(digits))
    return mant + "e" + ("-" if e < 0 else "+") + "%02d" % abs(e)


def _value(v):
    if v is None:
        return "null"
    if v is True:
        return "true"
    if v is False:
        return "false"
    if isinstance(v, str):
        return _string(v)
    if isinstance(v, int):
        return str(v)
    if isinstance(v, float):
        return _float(v)
    if isinstance(v, list):
        return "[" + ",".join(_value(x) for x in v) + "]"
    raise TypeError(v)


_FILES = ["path", "sha256"]
_COST = ["usd", "input_tokens", "cache_read_input_tokens", "cache_creation_input_tokens", "output_tokens", "turns"]
_ENTRY = ["seq", "prev_hash", "hash", "at", "stage", "role", "attempt", "family", "image", "image_digest", "argv_sha256", "inputs", "outputs",
          "network", "env_keys", "exit", "status", "cost"]


def _obj(o, keys, optional=()):
    parts = []
    for k in keys:
        if k in optional and (k not in o or o[k] in (0, 0.0) and not isinstance(o[k], bool)):
            continue
        parts.append("%s:%s" % (_string(k), _value(o[k])))
    return "{" + ",".join(parts) + "}"


def entry_text(entry):
    """RFC-0007 Part B, the ledger: the text of an entry that is hashed, with `hash` written as an empty string."""
    e = dict(entry, hash="")
    parts = []
    for k in _ENTRY:
        if k == "cost":
            c = e.get("cost")
            parts.append('"cost":' + ("null" if c is None else _obj(c, _COST, optional=_COST[1:])))
        elif k in ("inputs", "outputs"):
            parts.append("%s:[%s]" % (_string(k), ",".join(_obj(f, _FILES) for f in e.get(k) or [])))
        elif k == "env_keys":
            parts.append('"env_keys":' + _value(e.get(k) or []))
        else:
            parts.append("%s:%s" % (_string(k), _value(e.get(k))))
    if "egress_proxy" in e:
        parts.append('"egress_proxy":' + _obj(e["egress_proxy"], ["image", "image_digest"]))
    if "authorization" in e:
        parts.append('"authorization":' + _obj(e["authorization"], ["target", "id", "sha256"]))
    return "{" + ",".join(parts) + "}"


def entry_hash(entry):
    prev = entry.get("prev_hash") or ""
    return "sha256:" + hashlib.sha256((prev + entry_text(entry)).encode("utf-8")).hexdigest()


_AUTH = ["base_ref", "intent_sha256", "targets", "decision", "expires_at", "principal", "signature"]


def authorization_text(au):
    """The compact text of an authorization, keys in the order of authorization.schema.json."""
    return _obj(au, _AUTH)


def authorization_ref(record):
    """What the chained entry holds for a record of authorizations_used."""
    au = record["authorization"]
    text = authorization_payload(au["base_ref"], au["intent_sha256"], au["decision"], au["expires_at"], au["targets"])
    return {"target": record["target"], "id": hashlib.sha256(text).hexdigest(), "sha256": "sha256:" + hashlib.sha256(authorization_text(au).encode("utf-8")).hexdigest()}
