"""Puts one pinned revision of the Whisper model in the Hugging Face cache.

Runs as the whisper pod's init container, in the Speaches image. The model
is downloaded once into the cache on the PersistentVolumeClaim; later starts
find it there and download nothing. refs/main is pointed at the pinned
revision, so Speaches, which runs offline (HF_HUB_OFFLINE=1) and asks for
the model by name, loads exactly that revision. Other revisions left from
an earlier pin are removed.
"""

import os
import pathlib

import huggingface_hub
from huggingface_hub import constants

repo = os.environ["WHISPER_MODEL"]
revision = os.environ["WHISPER_MODEL_REVISION"]
# The files faster-whisper loads, and the card Speaches reads.
files = os.environ.get(
    "WHISPER_MODEL_FILES",
    "config.json,preprocessor_config.json,model.bin,tokenizer.json,vocabulary.*,README.md",
).split(",")

repo_dir = pathlib.Path(constants.HF_HUB_CACHE) / ("models--" + repo.replace("/", "--"))
snapshot = repo_dir / "snapshots" / revision
if all((snapshot / f).exists() for f in files if "*" not in f):
    print(f"{repo}@{revision}: already in the cache")
else:
    print(f"{repo}@{revision}: downloading")
    huggingface_hub.snapshot_download(repo_id=repo, revision=revision, allow_patterns=files)

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
