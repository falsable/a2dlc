"""Reference reading of RFC-0001 v3.5 §6.4 (the normative receipt), written from the RFC text alone: the tree_hash of a
commit, the WaiverSHA and the signed approval text. It exists to prove the RFC is enough to reproduce the binary's bytes
(e2e/test_rfc_reference.py compares it with Go's output) and to compute the expected values of the conformance vectors
(e2e/make_conformance_vectors.py). It is not part of the conformance kit and no verifier needs it."""
import hashlib
import subprocess

# RFC §6.4.2: the gate outputs and the signatures. Each entry is an exact path or a directory (prefix at a "/").
EXCLUDED = [".a2dlc/verdict.json", ".a2dlc/reports", ".a2dlc/approvals.json", ".a2dlc/escalations.json", ".a2dlc/run"]

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
