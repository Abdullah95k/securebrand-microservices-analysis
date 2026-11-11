"""Puts one pinned revision of the Whisper model in the Hugging Face cache.

Runs as the whisper pod's init container, in the Speaches image. The model
is downloaded once into the cache on the PersistentVolumeClaim; later starts
check it and download nothing. Each file is checked against the Hub's list
for that revision: present, of the Hub's size, and with the Hub's checksum
(SHA-256 for the large files, the git blob hash for the others); without the
Hub, each must be present and not empty. A copy that fails the check (a
missing vocabulary, a file cut short) is set aside outside the cache, not
used, and the revision is downloaded again. refs/main is pointed at the pinned revision, so Speaches,
which runs offline (HF_HUB_OFFLINE=1) and asks for the model by name, loads
exactly that revision. Other revisions left from an earlier pin are removed.

On 11 Nov 2026 the first server's copy had been failing since 7 Nov
("Cannot load the target vocabulary from the model directory") while the
old check, which skipped vocabulary.*, called it complete.
"""

import fnmatch
import hashlib
import os
import pathlib
import shutil
import sys
import time

import huggingface_hub
from huggingface_hub import HfApi, constants

repo = os.environ["WHISPER_MODEL"]
revision = os.environ["WHISPER_MODEL_REVISION"]
# The files faster-whisper loads, and the card Speaches reads.
patterns = os.environ.get(
    "WHISPER_MODEL_FILES",
    "config.json,preprocessor_config.json,model.bin,tokenizer.json,vocabulary.*,README.md",
).split(",")

repo_dir = pathlib.Path(constants.HF_HUB_CACHE) / ("models--" + repo.replace("/", "--"))
snapshot = repo_dir / "snapshots" / revision


def expected():
    """The revision's files we need and their sizes, from the Hub; None offline."""
    try:
        info = HfApi().model_info(repo, revision=revision, files_metadata=True, timeout=30)
    except Exception as e:  # noqa: BLE001 - offline or the Hub down: check what is here
        print(f"{repo}@{revision}: the Hub's file list is out of reach ({e}); checking the files here only")
        return None
    want = {s.rfilename: s for s in info.siblings if any(fnmatch.fnmatch(s.rfilename, p) for p in patterns)}
    for p in patterns:
        if not any(fnmatch.fnmatch(name, p) for name in want):
            sys.exit(f"{repo}@{revision}: the Hub has no file for {p}")
    return want


def same_content(f, sib):
    """Whether a file's content is the Hub's: SHA-256 for a large (LFS) file,
    the git blob hash for the others."""
    if sib.lfs is not None:
        h, want = hashlib.sha256(), sib.lfs.sha256
    else:
        h, want = hashlib.sha1(), sib.blob_id  # noqa: S324 - git's own blob hash
        h.update(b"blob %d\0" % f.stat().st_size)
    if not want:
        return True
    with open(f, "rb") as r:
        for chunk in iter(lambda: r.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest() == want


def problems(want):
    """What is wrong with the copy on the volume; empty when it is complete."""
    if not snapshot.is_dir():
        return ["not downloaded"]
    found = []
    if want is None:
        for p in patterns:
            matches = sorted(snapshot.glob(p))
            if not matches:
                found.append(f"{p}: missing")
            for f in matches:
                # exists() follows the snapshot's link to its blob.
                if not f.exists() or f.stat().st_size == 0:
                    found.append(f"{f.name}: missing or empty")
        return found
    for name, sib in sorted(want.items()):
        f = snapshot / name
        if not f.exists():
            found.append(f"{name}: missing")
        elif sib.size is not None and f.stat().st_size != sib.size:
            found.append(f"{name}: {f.stat().st_size} bytes, the Hub has {sib.size}")
        elif not same_content(f, sib):
            found.append(f"{name}: content differs from the Hub's")
    return found


want = expected()
bad = problems(want)
if not bad:
    print(f"{repo}@{revision}: in the cache, checked")
else:
    print(f"{repo}@{revision}: " + "; ".join(bad))
    if repo_dir.exists() and bad != ["not downloaded"]:
        # Set the faulty copy aside, once, outside the cache (Speaches lists
        # the cache): it is only a cache, but kept to look at. A later
        # faulty copy, with one already aside, is removed.
        kept = pathlib.Path(constants.HF_HUB_CACHE).parent / "faulty-models"
        aside = sorted(kept.glob(repo_dir.name + "-*"))
        if aside:
            shutil.rmtree(repo_dir)
            print(f"removed a faulty copy ({aside[0].name} already kept in {kept})")
        else:
            kept.mkdir(parents=True, exist_ok=True)
            target = kept / f"{repo_dir.name}-{int(time.time())}"
            repo_dir.rename(target)
            print(f"set the faulty copy aside as {target}")
    print(f"{repo}@{revision}: downloading")
    huggingface_hub.snapshot_download(repo_id=repo, revision=revision, allow_patterns=patterns)
    bad = problems(want)
    if bad:
        sys.exit(f"{repo}@{revision}: still faulty after downloading: " + "; ".join(bad))
    print(f"{repo}@{revision}: downloaded, checked")

ref = repo_dir / "refs" / "main"
ref.parent.mkdir(parents=True, exist_ok=True)
ref.write_text(revision)

old = [
    r.commit_hash
    for info in huggingface_hub.scan_cache_dir().repos
    if info.repo_id == repo
    for r in info.revisions
    if r.commit_hash != revision
]
if old:
    huggingface_hub.scan_cache_dir().delete_revisions(*old).execute()
    print(f"removed older revisions: {old}")
print(f"{repo}: main -> {revision}")
