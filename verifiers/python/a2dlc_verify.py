#!/usr/bin/env python3
"""Clean-room verifier for the A2-DLC receipt (RFC-0001 v3.5, section 6.4.8).

    a2dlc_verify.py verify [--commit C] [--contracts DIR]

Run it inside the repository. It reads .a2dlc/verdict.json from the working tree, and the waivers, the approvals and (for
notes) the config at the receipt's base_ref from the objects of the commit. It runs no engine and writes nothing. Exit 0 and
"a2dlc verify: ok" when the receipt is valid for the commit; otherwise one line per problem on standard error and exit 1;
exit 2 when the verifier itself fails.

Python standard library only, plus the `git` program to read objects. Signatures are checked in this file (Ed25519 from
RFC 8032, SSH signature format from OpenSSH PROTOCOL.sshsig), so ssh-keygen is not needed. Written from docs/A2-DLC_v3.5_protocol.md,
contracts/ and conformance/ alone.
"""
import argparse
import base64
import datetime
import fnmatch
import hashlib
import json
import os
import re
import struct
import subprocess
import sys

NAMESPACE = "a2dlc-approval"
EXCLUDED = (b".a2dlc/verdict.json", b".a2dlc/reports", b".a2dlc/approvals.json", b".a2dlc/escalations.json", b".a2dlc/run")
RECEIPT = ".a2dlc/verdict.json"
WAIVERS = ".a2dlc/waivers.json"
APPROVALS = ".a2dlc/approvals.json"
CONFIG = ".a2dlc/config.json"


class VerifierError(Exception):
    """The verifier cannot do its job (no repository, no git, unknown commit)."""


# ----- Ed25519 (RFC 8032, section 5.1) --------------------------------------------------------------------------------

_P = 2**255 - 19
_Q = 2**252 + 27742317777372353535851937790883648493
_D = -121665 * pow(121666, _P - 2, _P) % _P
_I = pow(2, (_P - 1) // 4, _P)  # sqrt(-1)


def _recover_x(y, sign):
    if y >= _P:
        return None
    x2 = (y * y - 1) * pow(_D * y * y + 1, _P - 2, _P) % _P
    if x2 == 0:
        return None if sign else 0
    x = pow(x2, (_P + 3) // 8, _P)
    if (x * x - x2) % _P != 0:
        x = x * _I % _P
    if (x * x - x2) % _P != 0:
        return None
    if (x & 1) != sign:
        x = _P - x
    return x


_GY = 4 * pow(5, _P - 2, _P) % _P
_GX = _recover_x(_GY, 0)
_G = (_GX, _GY, 1, _GX * _GY % _P)


def _decode_point(s):
    if len(s) != 32:
        return None
    y = int.from_bytes(s, "little")
    sign = y >> 255
    y &= (1 << 255) - 1
    x = _recover_x(y, sign)
    return None if x is None else (x, y, 1, x * y % _P)


def _point_add(a, b):
    A = (a[1] - a[0]) * (b[1] - b[0]) % _P
    B = (a[1] + a[0]) * (b[1] + b[0]) % _P
    C = 2 * a[3] * b[3] * _D % _P
    D = 2 * a[2] * b[2] % _P
    E, F, G, H = B - A, D - C, D + C, B + A
    return (E * F % _P, G * H % _P, F * G % _P, E * H % _P)


def _point_mul(s, point):
    q = (0, 1, 1, 0)
    while s > 0:
        if s & 1:
            q = _point_add(q, point)
        point = _point_add(point, point)
        s >>= 1
    return q


def _point_equal(a, b):
    return (a[0] * b[2] - b[0] * a[2]) % _P == 0 and (a[1] * b[2] - b[1] * a[2]) % _P == 0


def ed25519_verify(public, message, signature):
    """True when `signature` (64 bytes) is a valid Ed25519 signature of `message` under the 32-byte key `public`."""
    if len(public) != 32 or len(signature) != 64:
        return False
    a = _decode_point(public)
    r = _decode_point(signature[:32])
    if a is None or r is None:
        return False
    s = int.from_bytes(signature[32:], "little")
    if s >= _Q:
        return False
    h = int.from_bytes(hashlib.sha512(signature[:32] + public + message).digest(), "little") % _Q
    return _point_equal(_point_mul(s, _G), _point_add(r, _point_mul(h, a)))


# ----- SSH signatures and allowed signers -------------------------------------------------------------------------------


class _Reader:
    def __init__(self, data):
        self.data, self.pos = data, 0

    def take(self, n):
        if n < 0 or self.pos + n > len(self.data):
            raise ValueError("truncated")
        out = self.data[self.pos:self.pos + n]
        self.pos += n
        return out

    def string(self):
        return self.take(struct.unpack(">I", self.take(4))[0])

    def done(self):
        return self.pos == len(self.data)


def _ssh_string(b):
    return struct.pack(">I", len(b)) + b


def parse_armored_signature(text):
    """Binary blob of an armored OpenSSH signature, or None."""
    lines = text.replace("\r\n", "\n").split("\n")
    lines = [l for l in lines if l != ""]
    if len(lines) < 3 or lines[0] != "-----BEGIN SSH SIGNATURE-----" or lines[-1] != "-----END SSH SIGNATURE-----":
        return None
    try:
        return base64.b64decode("".join(lines[1:-1]), validate=True)
    except ValueError:
        return None


def _pattern_list_matches(patterns, value):
    """ssh pattern list: comma separated, * and ?, a leading ! negates. At least one positive match and no negated match."""
    positive = False
    for pat in patterns.split(","):
        if pat.startswith("!"):
            if fnmatch.fnmatchcase(value, pat[1:]):
                return False
        elif fnmatch.fnmatchcase(value, pat):
            positive = True
    return positive


def _split_fields(line):
    """Whitespace separated fields of an allowed-signers line; double quotes group and stay in the field."""
    fields, cur, quoted = [], [], False
    for ch in line:
        if ch == '"':
            quoted = not quoted
            cur.append(ch)
        elif ch in " 	" and not quoted:
            if cur:
                fields.append("".join(cur))
                cur = []
        else:
            cur.append(ch)
    if quoted:
        return None
    if cur:
        fields.append("".join(cur))
    return fields


def _split_options(s):
    """Comma separated options outside double quotes, as (name, value without quotes)."""
    parts, cur, quoted = [], [], False
    for ch in s:
        if ch == '"':
            quoted = not quoted
        elif ch == "," and not quoted:
            parts.append("".join(cur))
            cur = []
        else:
            cur.append(ch)
    parts.append("".join(cur))
    return [tuple(p.partition("=")[::2]) if "=" in p else (p, None) for p in parts if p]


def parse_allowed_signers(text):
    """[(principals, options, keytype, key blob)] for the lines this verifier can honour.

    A line with an option it does not implement (anything but `namespaces=`), a certificate authority, or a malformed key is
    dropped: the verifier then approves less than ssh-keygen would, never more."""
    out = []
    for raw in text.splitlines():
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        fields = _split_fields(line)
        if not fields or len(fields) < 3:
            continue
        principals, rest = fields[0].replace('"', ""), fields[1:]
        options = {}
        if not re.match(r"^(ssh-|ecdsa-|sk-)", rest[0]):
            parsed = _split_options(rest[0])
            if any(name != "namespaces" or val is None for name, val in parsed) or len(rest) < 3:
                continue
            options = dict(parsed)
            rest = rest[1:]
        keytype = rest[0]
        try:
            blob = base64.b64decode(rest[1], validate=True)
        except ValueError:
            continue
        out.append((principals, options, keytype, blob))
    return out


def load_allowed_signers(environ=None):
    """Text of the allowed-signers file, in the order of RFC 6.4.7; None when there is none."""
    env = os.environ if environ is None else environ
    path = env.get("A2DLC_ALLOWED_SIGNERS")
    if path:
        try:
            with open(path, "r", encoding="utf-8") as f:
                return f.read()
        except OSError:
            return None
    data = env.get("A2DLC_ALLOWED_SIGNERS_DATA")
    if data:
        return data
    home = os.path.join(os.path.expanduser("~"), ".config", "a2dlc", "allowed_signers")
    try:
        with open(home, "r", encoding="utf-8") as f:
            return f.read()
    except OSError:
        return None


def check_signature(signers_text, principal, signature_text, message):
    """(ok, reason) for an armored SSH signature over `message` by `principal` in namespace a2dlc-approval."""
    if signers_text is None:
        return False, "no allowed signers configured"
    blob = parse_armored_signature(signature_text)
    if blob is None:
        return False, "the signature is not an armored SSH signature"
    try:
        r = _Reader(blob)
        if r.take(6) != b"SSHSIG":
            return False, "bad magic"
        if struct.unpack(">I", r.take(4))[0] != 1:
            return False, "unsupported signature version"
        pub = r.string()
        namespace = r.string()
        r.string()  # reserved
        hash_alg = r.string()
        sig = r.string()
        if not r.done():
            return False, "trailing bytes in the signature"
        pr = _Reader(pub)
        pub_type, pub_key = pr.string(), pr.string()
        sr = _Reader(sig)
        sig_type, sig_raw = sr.string(), sr.string()
    except (ValueError, struct.error):
        return False, "malformed signature"
    if namespace != NAMESPACE.encode():
        return False, "signature namespace is %r, not %s" % (namespace.decode("utf-8", "replace"), NAMESPACE)
    if pub_type != b"ssh-ed25519" or sig_type != b"ssh-ed25519":
        return False, "key type %s is not supported (only ssh-ed25519)" % pub_type.decode("utf-8", "replace")
    if hash_alg == b"sha512":
        digest = hashlib.sha512(message).digest()
    elif hash_alg == b"sha256":
        digest = hashlib.sha256(message).digest()
    else:
        return False, "unsupported hash algorithm"
    signed = b"SSHSIG" + _ssh_string(namespace) + _ssh_string(b"") + _ssh_string(hash_alg) + _ssh_string(digest)
    allowed = False
    for principals, options, keytype, key_blob in parse_allowed_signers(signers_text):
        if keytype != "ssh-ed25519" or key_blob != pub:
            continue
        if not _pattern_list_matches(principals, principal):
            continue
        if "namespaces" in options and not _pattern_list_matches(options["namespaces"], NAMESPACE):
            continue
        allowed = True
        break
    if not allowed:
        return False, "no allowed-signers line lists this key for %s in namespace %s" % (principal, NAMESPACE)
    if not ed25519_verify(pub_key, signed, sig_raw):
        return False, "the signature does not verify"
    return True, ""


# ----- JSON Schema (the subset contracts/ uses, draft 2020-12) -----------------------------------------------------------


def json_equal(a, b):
    if isinstance(a, bool) or isinstance(b, bool):
        return isinstance(a, bool) and isinstance(b, bool) and a == b
    if isinstance(a, (int, float)) and isinstance(b, (int, float)):
        return a == b
    if isinstance(a, list) and isinstance(b, list):
        return len(a) == len(b) and all(json_equal(x, y) for x, y in zip(a, b))
    if isinstance(a, dict) and isinstance(b, dict):
        return a.keys() == b.keys() and all(json_equal(a[k], b[k]) for k in a)
    return type(a) is type(b) and a == b


def _is_type(inst, t):
    if t == "object":
        return isinstance(inst, dict)
    if t == "array":
        return isinstance(inst, list)
    if t == "string":
        return isinstance(inst, str)
    if t == "boolean":
        return isinstance(inst, bool)
    if t == "null":
        return inst is None
    if t == "integer":
        return (isinstance(inst, int) and not isinstance(inst, bool)) or (isinstance(inst, float) and inst.is_integer())
    if t == "number":
        return isinstance(inst, (int, float)) and not isinstance(inst, bool)
    return False


_DATE_TIME = re.compile(r"^(\d{4})-(\d{2})-(\d{2})[Tt](\d{2}):(\d{2}):(\d{2})(\.\d+)?([Zz]|[+-]\d{2}:\d{2})$", re.ASCII)


def _valid_date_time(s):
    m = _DATE_TIME.match(s)
    if not m:
        return False
    try:
        datetime.date(int(m[1]), int(m[2]), int(m[3]))
    except ValueError:
        return False
    if int(m[4]) > 23 or int(m[5]) > 59 or int(m[6]) > 60:
        return False
    if m[8] not in ("Z", "z") and (int(m[8][1:3]) > 23 or int(m[8][4:6]) > 59):
        return False
    return True


def _ecma_pattern(pattern):
    # ECMAScript's `$` matches only at the very end; Python's also before a final newline.
    if pattern.endswith("$") and not pattern.endswith("\\$"):
        return pattern[:-1] + r"\Z"
    return pattern


class Schemas:
    """The schemas of a contracts/ directory, resolved by file name."""

    def __init__(self, directory):
        self.docs = {}
        for name in os.listdir(directory):
            if name.endswith(".schema.json"):
                with open(os.path.join(directory, name), "r", encoding="utf-8") as f:
                    self.docs[name[:-len(".schema.json")]] = json.load(f)

    def validate(self, name, instance):
        """List of error messages; empty when `instance` is valid under <name>.schema.json."""
        errors, _ = self._check(self.docs[name], instance, name, "$")
        return errors

    def _resolve(self, ref, current):
        file_part, _, pointer = ref.partition("#")
        name = current if not file_part else file_part[:-len(".schema.json")] if file_part.endswith(".schema.json") else file_part
        node = self.docs[name]
        for token in [t for t in pointer.split("/") if t]:
            node = node[token.replace("~1", "/").replace("~0", "~")]
        return node, name

    def _check(self, schema, inst, current, path):
        """(errors, evaluated property names of inst)."""
        if schema is True:
            return [], set()
        if schema is False:
            return ["%s: not allowed" % path], set()
        errors, evaluated = [], set()

        def sub(s, i, p, cur=current):
            e, ev = self._check(s, i, cur, p)
            return e, ev

        if "$ref" in schema:
            target, name = self._resolve(schema["$ref"], current)
            e, ev = sub(target, inst, path, name)
            errors += e
            evaluated |= ev
        if "type" in schema:
            types = schema["type"] if isinstance(schema["type"], list) else [schema["type"]]
            if not any(_is_type(inst, t) for t in types):
                errors.append("%s: expected %s" % (path, "/".join(types)))
                return errors, evaluated
        if "const" in schema and not json_equal(inst, schema["const"]):
            errors.append("%s: must be %r" % (path, schema["const"]))
        if "enum" in schema and not any(json_equal(inst, v) for v in schema["enum"]):
            errors.append("%s: %r is not one of %s" % (path, inst, schema["enum"]))
        if isinstance(inst, str):
            if "minLength" in schema and len(inst) < schema["minLength"]:
                errors.append("%s: shorter than %d" % (path, schema["minLength"]))
            if "pattern" in schema and not re.search(_ecma_pattern(schema["pattern"]), inst):
                errors.append("%s: does not match %s" % (path, schema["pattern"]))
            if schema.get("format") == "date-time" and not _valid_date_time(inst):
                errors.append("%s: not an RFC 3339 date-time" % path)
        if isinstance(inst, (int, float)) and not isinstance(inst, bool):
            if "minimum" in schema and inst < schema["minimum"]:
                errors.append("%s: below %s" % (path, schema["minimum"]))
            if "exclusiveMinimum" in schema and inst <= schema["exclusiveMinimum"]:
                errors.append("%s: not above %s" % (path, schema["exclusiveMinimum"]))
        if isinstance(inst, list):
            if "minItems" in schema and len(inst) < schema["minItems"]:
                errors.append("%s: fewer than %d items" % (path, schema["minItems"]))
            if "maxItems" in schema and len(inst) > schema["maxItems"]:
                errors.append("%s: more than %d items" % (path, schema["maxItems"]))
            if schema.get("uniqueItems") and any(json_equal(inst[i], inst[j]) for i in range(len(inst)) for j in range(i)):
                errors.append("%s: items are not unique" % path)
            if "items" in schema:
                for i, item in enumerate(inst):
                    errors += sub(schema["items"], item, "%s[%d]" % (path, i))[0]
        if isinstance(inst, dict):
            props = schema.get("properties", {})
            for key in schema.get("required", []):
                if key not in inst:
                    errors.append("%s: missing required %s" % (path, key))
            for key, value in inst.items():
                if key in props:
                    evaluated.add(key)
                    errors += sub(props[key], value, "%s.%s" % (path, key))[0]
            if "additionalProperties" in schema:
                for key, value in inst.items():
                    if key not in props:
                        evaluated.add(key)
                        e = sub(schema["additionalProperties"], value, "%s.%s" % (path, key))[0]
                        errors += e if e else []
        for s in schema.get("allOf", []):
            e, ev = sub(s, inst, path)
            errors += e
            evaluated |= ev
        if "oneOf" in schema:
            results = [sub(s, inst, path) for s in schema["oneOf"]]
            valid = [r for r in results if not r[0]]
            if len(valid) != 1:
                errors.append("%s: matches %d of the oneOf alternatives" % (path, len(valid)))
            for r in valid:
                evaluated |= r[1]
        if "if" in schema:
            cond_errors, cond_ev = sub(schema["if"], inst, path)
            branch = schema.get("then") if not cond_errors else schema.get("else")
            if not cond_errors:
                evaluated |= cond_ev
            if branch is not None:
                e, ev = sub(branch, inst, path)
                errors += e
                evaluated |= ev
        if schema.get("unevaluatedProperties") is False and isinstance(inst, dict):
            for key in inst:
                if key not in evaluated:
                    errors.append("%s: unexpected property %s" % (path, key))
        return errors, evaluated


# ----- git ---------------------------------------------------------------------------------------------------------------


class Repo:
    def __init__(self, cwd):
        self.cwd = cwd
        try:
            self.top = self._git("rev-parse", "--show-toplevel").decode("utf-8", "replace").strip()
        except (OSError, subprocess.CalledProcessError) as e:
            raise VerifierError("not inside a git repository: %s" % e)

    def _git(self, *args, check=True):
        p = subprocess.run(["git", "-C", self.cwd, *args], stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        if check and p.returncode != 0:
            raise subprocess.CalledProcessError(p.returncode, args, p.stdout, p.stderr)
        return p.stdout if check else p

    def commit(self, rev):
        if rev.startswith("-"):
            raise VerifierError("bad commit %r" % rev)
        try:
            return self._git("rev-parse", "--verify", rev + "^{commit}").decode().strip()
        except subprocess.CalledProcessError:
            raise VerifierError("unknown commit %r" % rev)

    def entries(self, commit):
        """[(mode, oid hex, path bytes)] of every non-tree entry of the commit's tree."""
        out = []
        for rec in self._git("ls-tree", "-r", "-z", "--full-tree", commit).split(b"\0"):
            if not rec:
                continue
            head, _, path = rec.partition(b"\t")
            mode, _type, oid = head.split(b" ")
            out.append((mode.decode(), oid.decode(), path))
        return out

    def blob(self, commit, path):
        """Bytes of the file at `path` in `commit`, or None when it is not there."""
        p = self._git("cat-file", "blob", "%s:%s" % (commit, path), check=False)
        return p.stdout if p.returncode == 0 else None

    def is_ancestor(self, a, b):
        return self._git("merge-base", "--is-ancestor", a, b, check=False).returncode == 0

    def pushed_base(self):
        """The commit HEAD is compared against when pushing: its upstream, else its fork point from origin/HEAD."""
        for args in (("rev-parse", "--verify", "-q", "@{upstream}^{commit}"), ("merge-base", "HEAD", "refs/remotes/origin/HEAD")):
            p = self._git(*args, check=False)
            if p.returncode == 0 and p.stdout.strip():
                return p.stdout.decode().strip()
        return None


def excluded(path):
    return any(path == e or path.startswith(e + b"/") for e in EXCLUDED)


def tree_hash(entries):
    """Hex id of the git tree of `entries` without the excluded paths (RFC 6.4.2). The object format follows the oid length."""
    algo = hashlib.sha256 if entries and len(entries[0][1]) == 64 else hashlib.sha1
    root = {}
    for mode, oid, path in entries:
        if excluded(path):
            continue
        parts = path.split(b"/")
        node = root
        for part in parts[:-1]:
            node = node.setdefault(part, {})
        node[parts[-1]] = (mode, bytes.fromhex(oid))
    return _tree_id(root, algo)


def _tree_id(node, algo):
    body = b""
    for name in sorted(node, key=lambda n: n + b"/" if isinstance(node[n], dict) else n):
        child = node[name]
        if isinstance(child, dict):
            body += b"40000 " + name + b"\0" + bytes.fromhex(_tree_id(child, algo))
        else:
            body += child[0].encode() + b" " + name + b"\0" + child[1]
    return algo(b"tree %d\0" % len(body) + body).hexdigest()


# ----- waivers, approvals, notes ----------------------------------------------------------------------------------------

_SPECIAL = {"\b": "\\b", "\f": "\\f", "\n": "\\n", "\r": "\\r", "\t": "\\t", '"': '\\"', "\\": "\\\\"}


def _go_json_string(s):
    out = ['"']
    for ch in s:
        if ch in _SPECIAL:
            out.append(_SPECIAL[ch])
        elif ch in "<>&\u2028\u2029" or ord(ch) < 0x20:
            out.append("\\u%04x" % ord(ch))
        elif 0xD800 <= ord(ch) <= 0xDFFF:
            out.append("�")
        else:
            out.append(ch)
    out.append('"')
    return "".join(out)


def waiver_sha256(target, decision, reason):
    """WaiverSHA of RFC 6.4.6: SHA-256 of {"target":T,"decision":D,"reason":R} written the way Go's encoding/json writes it."""
    text = '{"target":%s,"decision":%s,"reason":%s}' % (_go_json_string(target), _go_json_string(decision), _go_json_string(reason))
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


def signed_text(target, waiver_hash, file_blob):
    return ("a2dlc-approval/1\n%s\n%s\n%s\n" % (target, waiver_hash, file_blob)).encode("utf-8")


def _glob_half(glob):
    out, i = [], 0
    while i < len(glob):
        if glob.startswith("**/", i):
            out.append("(?:.*/)?")
            i += 3
        elif glob.startswith("**", i):
            out.append(".*")
            i += 2
        elif glob[i] == "*":
            out.append("[^/]*")
            i += 1
        elif glob[i] == "?":
            out.append("[^/]")
            i += 1
        else:
            out.append(re.escape(glob[i]))
            i += 1
    return re.compile("".join(out), re.DOTALL)


def matches_critical(glob, target):
    """Whether the critical_code glob names the target `<file>::<symbol>` (RFC 6.4.5)."""
    file_part, sep, symbol = target.partition("::")
    gfile, gsep, gsymbol = glob.partition("::")
    if not _glob_half(gfile).fullmatch(file_part):
        return False
    if not gsep:
        return True
    return bool(sep) and _glob_half(gsymbol).fullmatch(symbol) is not None


def _strict_json(data):
    """Parsed JSON of UTF-8 bytes; ValueError for anything but one complete JSON document."""

    def reject(name):
        raise ValueError("%s is not JSON" % name)

    return json.loads(data.decode("utf-8"), parse_constant=reject)


class Problem:
    def __init__(self, kind, message):
        self.kind, self.message = kind, message

    def __str__(self):
        return "%s: %s" % (self.kind, self.message)


def waived_targets(receipt):
    """Targets of `targets` and `tests` with waived: true and findings of `checks` with waived: true, each once, in order."""
    seen = []

    def add(item):
        if isinstance(item, dict) and item.get("waived") is True and isinstance(item.get("target"), str) and item["target"] not in seen:
            seen.append(item["target"])

    for key in ("targets", "tests"):
        for item in receipt.get(key) or [] if isinstance(receipt.get(key), list) else []:
            add(item)
    for check in receipt.get("checks") or [] if isinstance(receipt.get("checks"), list) else []:
        if isinstance(check, dict) and isinstance(check.get("findings"), list):
            for finding in check["findings"]:
                add(finding)
    return seen


def verify(repo, rev, schemas, environ=None):
    """The problems found in the receipt for the commit `rev`; an empty list when it is valid."""
    problems = []
    commit = repo.commit(rev)
    path = os.path.join(repo.top, *RECEIPT.split("/"))
    try:
        with open(path, "rb") as f:
            raw = f.read()
    except OSError:
        return [Problem("receipt_missing", "there is no %s in the working tree" % RECEIPT)]
    try:
        receipt = _strict_json(raw)
    except ValueError as e:
        return [Problem("receipt_invalid", "%s is out of contract: not one JSON document (%s)" % (RECEIPT, str(e)[:80]))]
    errors = schemas.validate("verdict", receipt)
    if errors:
        problems.append(Problem("receipt_invalid", "%s is out of contract: %s" % (RECEIPT, "; ".join(errors[:3]))))
    if not isinstance(receipt, dict):
        return problems
    entries = repo.entries(commit)
    expected = "git-tree:" + tree_hash(entries)
    if receipt.get("tree_hash") != expected:
        problems.append(Problem("stale", "the verdict is stale: tree_hash is %s, the tree of %s is %s" % (receipt.get("tree_hash"), commit[:12], expected)))
    if receipt.get("verdict") != "PASS":
        problems.append(Problem("not_pass", "the verdict is %s, not PASS" % (receipt.get("verdict"),)))
    base = receipt.get("base_ref")
    pushed = repo.pushed_base()
    if pushed and isinstance(base, str) and base != pushed and not repo.is_ancestor(base, pushed):
        problems.append(Problem("base_unpushed", "base_ref %s is not in what was pushed (%s)" % (base, pushed[:12])))
    problems += _check_notes(repo, receipt, base)
    problems += _check_waivers(repo, commit, entries, receipt, schemas, environ)
    return problems


def _check_notes(repo, receipt, base):
    targets = receipt.get("targets")
    noted = [t for t in targets if isinstance(t, dict) and "note" in t] if isinstance(targets, list) else []
    if not noted:
        return []
    critical = _critical_code(repo, base)  # None: the config cannot be read, so everything is critical
    problems = []
    for t in noted:
        name = t.get("target")
        if t.get("verdict") != "FAIL" or not isinstance(t.get("counterexample"), dict) or t.get("waived") is not False:
            problems.append(Problem("note_invalid", "note on %s is not a behavior divergence: it needs verdict FAIL, a counterexample and no waiver" % name))
        if isinstance(name, str) and (critical is None or any(matches_critical(g, name) for g in critical)):
            problems.append(Problem("note_on_critical_code", "behavior divergence on critical code: %s" % name))
    return problems


def _critical_code(repo, base):
    if not isinstance(base, str):
        return None
    data = repo.blob(base, CONFIG)
    if data is None:
        return None
    try:
        config = _strict_json(data)
    except ValueError:
        return None
    if not isinstance(config, dict):
        return None
    globs = config.get("critical_code", [])
    if not isinstance(globs, list) or not all(isinstance(g, str) for g in globs):
        return None
    return globs


def _load_document(repo, commit, path, schemas, name):
    """(document, None) or (None, why) for a committed JSON document checked against its schema."""
    data = repo.blob(commit, path)
    if data is None:
        return None, "there is no %s in the commit" % path
    try:
        doc = _strict_json(data)
    except ValueError as e:
        return None, "%s is not valid JSON: %s" % (path, str(e)[:80])
    errors = schemas.validate(name, doc)
    if errors:
        return None, "%s is out of contract: %s" % (path, errors[0])
    return doc, None


def _check_waivers(repo, commit, entries, receipt, schemas, environ):
    waived = waived_targets(receipt)
    if not waived:
        return []
    waivers_doc, waivers_err = _load_document(repo, commit, WAIVERS, schemas, "waivers")
    approvals_doc, approvals_err = _load_document(repo, commit, APPROVALS, schemas, "approvals")
    waiver_by_target = {}
    for w in (waivers_doc or {}).get("waivers", []):
        waiver_by_target[w["target"]] = w  # the last one is the waiver
    blobs = {p: oid for _mode, oid, p in entries if not excluded(p)}
    signers = load_allowed_signers(environ)
    problems = []
    for target in waived:
        approvals = [a for a in (approvals_doc or {}).get("approvals", []) if a["target"] == target]
        if not approvals:
            problems.append(Problem("unsigned_waiver", "waiver for %s: unsigned: approve it (%s)" % (target, approvals_err or "no approval for this target")))
            continue
        reasons = []
        for a in approvals:
            ok, why = _approval_valid(a, waiver_by_target.get(target), waivers_err, blobs, signers)
            if ok:
                reasons = None
                break
            reasons.append(why)
        if reasons is not None:
            problems.append(Problem("unsigned_waiver", "waiver for %s: %s" % (target, " | ".join(reasons))))
    return problems


def _approval_valid(approval, waiver, waivers_err, blobs, signers):
    target = approval["target"]
    if waiver is None:
        return False, "the approval has no waiver in the commit (%s)" % (waivers_err or "no waiver for this target")
    if approval["waiver_sha256"] != waiver_sha256(waiver["target"], waiver["decision"], waiver["reason"]):
        return False, "the waiver changed after it was signed"
    blob = blobs.get(target.partition("::")[0].encode("utf-8"))
    if blob is None or approval["file_blob"] != blob:
        return False, "the file changed after signing (blob %s)" % (blob or "missing")
    ok, why = check_signature(signers, approval["principal"], approval["signature"], signed_text(target, approval["waiver_sha256"], approval["file_blob"]))
    if not ok:
        return False, "invalid signature for %s: %s" % (approval["principal"], why)
    return True, ""


def default_contracts():
    here = os.path.dirname(os.path.abspath(__file__))
    for candidate in (os.environ.get("A2DLC_CONTRACTS"), os.path.join(here, "contracts"), os.path.join(here, "..", "..", "contracts")):
        if candidate and os.path.isdir(candidate):
            return candidate
    raise VerifierError("contracts/ not found: pass --contracts DIR or set A2DLC_CONTRACTS")


def main(argv=None):
    ap = argparse.ArgumentParser(description="Clean-room verifier of the A2-DLC receipt (RFC-0001 v3.5 section 6.4.8)")
    sub = ap.add_subparsers(dest="command", required=True)
    v = sub.add_parser("verify", help="verify the receipt in .a2dlc/verdict.json for a commit")
    v.add_argument("--commit", default="HEAD")
    v.add_argument("--contracts", help="directory with the *.schema.json files (default: $A2DLC_CONTRACTS, or contracts/ of this checkout)")
    a = ap.parse_args(argv)
    try:
        schemas = Schemas(a.contracts or default_contracts())
        problems = verify(Repo(os.getcwd()), a.commit, schemas)
    except VerifierError as e:
        print("a2dlc verify: %s" % e, file=sys.stderr)
        return 2
    except Exception as e:  # a crash must not look like an invalid receipt
        print("a2dlc verify: internal error: %s: %s" % (type(e).__name__, e), file=sys.stderr)
        return 2
    if problems:
        for p in problems:
            print("a2dlc verify: %s" % p, file=sys.stderr)
        return 1
    print("a2dlc verify: ok")
    return 0


if __name__ == "__main__":
    sys.exit(main())
