#!/usr/bin/env python3
"""Compara transcripciones de vozgo contra referencias humanas (WER).

Estructura esperada:

    <dir>/ref/<nombre>.txt      transcripción humana
    <dir>/<modelo>/<nombre>.txt salida de vozgo (un directorio por variante)

    python3 scripts/wer.py testdata/wer

Normaliza a minúsculas y quita tildes y puntuación: interesa el contenido, no la
puntuación que whisper inventa.
"""

import re
import sys
import unicodedata
from pathlib import Path


def normalize(text: str) -> list[str]:
    text = text.lower()
    decomposed = unicodedata.normalize("NFD", text)
    # Conserva la ñ: se descompone en n + tilde, que no es un acento a quitar.
    stripped = []
    for ch in decomposed:
        if unicodedata.category(ch) == "Mn" and ch != "̃":
            continue
        stripped.append(ch)
    text = unicodedata.normalize("NFC", "".join(stripped))
    text = re.sub(r"[^0-9a-zñáéíóúü ]+", " ", text)
    return text.split()


def wer(ref: list[str], hyp: list[str]) -> float:
    """Distancia de edición por palabras, dividida por la longitud de la referencia."""
    prev = list(range(len(hyp) + 1))
    for i in range(1, len(ref) + 1):
        cur = [i] + [0] * len(hyp)
        for j in range(1, len(hyp) + 1):
            cost = 0 if ref[i - 1] == hyp[j - 1] else 1
            cur[j] = min(prev[j] + 1, cur[j - 1] + 1, prev[j - 1] + cost)
        prev = cur
    return prev[len(hyp)] / max(1, len(ref))


def main() -> int:
    root = Path(sys.argv[1] if len(sys.argv) > 1 else ".")
    refs = sorted((root / "ref").glob("*.txt"))
    if not refs:
        print(f"no hay referencias en {root / 'ref'}", file=sys.stderr)
        return 1

    variants = sorted(
        d.name for d in root.iterdir() if d.is_dir() and d.name not in ("ref", "in")
    )
    print(f"{'archivo':14} {'variante':10} {'palabras':>8} {'WER':>7} {'acierto':>8}")
    for ref_path in refs:
        ref = normalize(ref_path.read_text())
        for variant in variants:
            hyp_path = root / variant / ref_path.name
            if not hyp_path.exists():
                continue
            err = wer(ref, normalize(hyp_path.read_text()))
            print(f"{ref_path.stem:14} {variant:10} {len(ref):>8} {err:>6.1%} {1 - err:>7.1%}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
