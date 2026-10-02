# Звуки меню приложения Kinodom (план 16В): синтез, без чужих файлов. Пересобрать — python make-sounds.py; файлы ложатся
# в app/src/main/res/raw. WAV 44,1 кГц, 16 бит, моно; пик — около −14 dBFS (упор — тише), фронт 2 мс и спад 3 мс в
# конце — без щелчков.
import math
import os
import struct
import wave

RATE = 44100
OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "app", "src", "main", "res", "raw")


def tone(dur, f0, f1, decay, harm=0.0, start=0.0):
    """Синус с плавным переходом частоты f0 → f1 и экспоненциальным затуханием; start — задержка начала, с."""
    n = int(RATE * (start + dur))
    out = [0.0] * n
    phase = 0.0
    for i in range(int(RATE * dur)):
        t = i / RATE
        f = f0 + (f1 - f0) * (t / dur)
        phase += 2 * math.pi * f / RATE
        a = min(1.0, t / 0.002) * math.exp(-t / decay)
        out[int(RATE * start) + i] = a * (math.sin(phase) + harm * math.sin(2 * phase))
    return out


def mix(*parts):
    n = max(len(p) for p in parts)
    return [sum(p[i] for p in parts if i < len(p)) for i in range(n)]


def write(name, samples, peak_db):
    tail = int(RATE * 0.003)
    for i in range(tail):
        samples[-tail + i] *= 1 - i / tail
    top = max(abs(s) for s in samples) or 1.0
    k = 10 ** (peak_db / 20) / top
    data = b"".join(struct.pack("<h", int(max(-1.0, min(1.0, s * k)) * 32767)) for s in samples)
    with wave.open(os.path.join(OUT, name + ".wav"), "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(RATE)
        w.writeframes(data)


def main():
    os.makedirs(OUT, exist_ok=True)
    # Шаг фокуса — короткий мягкий щелчок.
    write("nav_move", tone(0.028, 1650, 1450, 0.008, harm=0.15), -14)
    # OK — двухнотный перезвон (си и ми).
    write("nav_select", mix(tone(0.14, 988, 988, 0.05, harm=0.1), tone(0.095, 1319, 1319, 0.045, harm=0.1, start=0.045)), -14)
    # «Назад» — нисходящий тон.
    write("nav_back", tone(0.09, 1100, 700, 0.03, harm=0.1), -14)
    # Упор — глухой низкий, тише остальных.
    write("nav_edge", tone(0.06, 220, 200, 0.02, harm=0.3), -20)


if __name__ == "__main__":
    main()
