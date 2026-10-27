"""M7 voice: the owner's spoken words into the Compass loop.

No microphone capture of our own: the clip comes from the Muse app (or any
recorder). `muse voice transcribe` turns it into text; ingest generalizes
and proposes; the owner confirms with a tap (`POST /proposals/.../confirm`)
or in the terminal (`pengu confirm`). `pengu voice --clip f.m4a` runs the
whole path and prints the proposal.
"""

import subprocess


def transcribe(clip):
    """Text of an audio clip via `muse voice transcribe`. Raises on failure."""
    try:
        p = subprocess.run(["muse", "voice", "transcribe", clip],
                           capture_output=True, text=True, timeout=120)
    except (OSError, subprocess.TimeoutExpired) as e:
        raise RuntimeError(f"muse voice failed: {e}") from e
    text = (p.stdout or "").strip()
    if p.returncode != 0 or not text:
        raise RuntimeError(
            "muse voice exited %d: %s" % (p.returncode, (p.stderr or "").strip()[:200]))
    return text
