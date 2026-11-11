"""The whisper server's startup check: it must transcribe, not only answer.

Speaches answers /health before, and whether or not, its model loads: on
11 Nov 2026 a server that couldn't load its model had been healthy, and
answering every transcription with 500, since 7 Nov. This check sends one
second of silence to /v1/audio/transcriptions with the pinned model (the
first request loads it, which can take a minute) and succeeds only on a
200 with a JSON answer. Until it does, the pod isn't started, so isn't
Ready: its Service has no endpoint, and the analysis passes over it.

Usage: whisper-ready.py MODEL_ID
"""

import io
import json
import sys
import urllib.request
import uuid
import wave

model = sys.argv[1]
audio = io.BytesIO()
with wave.open(audio, "wb") as w:
    w.setnchannels(1)
    w.setsampwidth(2)
    w.setframerate(16000)
    w.writeframes(b"\0\0" * 16000)

boundary = uuid.uuid4().hex
body = (
    (
        f'--{boundary}\r\nContent-Disposition: form-data; name="model"\r\n\r\n{model}\r\n'
        f'--{boundary}\r\nContent-Disposition: form-data; name="response_format"\r\n\r\njson\r\n'
        f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="ready.wav"\r\n'
        "Content-Type: audio/wav\r\n\r\n"
    ).encode()
    + audio.getvalue()
    + f"\r\n--{boundary}--\r\n".encode()
)
req = urllib.request.Request(
    "http://127.0.0.1:8000/v1/audio/transcriptions",
    data=body,
    headers={"Content-Type": f"multipart/form-data; boundary={boundary}"},
)
try:
    with urllib.request.urlopen(req, timeout=170) as r:  # noqa: S310 - our own pod
        json.load(r)
except Exception as e:  # noqa: BLE001 - any failure: not ready yet
    print(f"whisper can't transcribe yet: {e}", file=sys.stderr)
    sys.exit(1)
print("whisper transcribes")
