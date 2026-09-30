# Нарезка логотипа Kinodom (source.jpg — логотип заказчика, 2026-09-30) на файлы программы:
#   шапка пульта — иконка и надпись по отдельности, фон прозрачный (шапка тёмная, не чёрная);
#   ярлыки, favicon, установщик, значок в трее — только иконка в квадрате на чёрном фоне.
# Запуск из корня репозитория: python assets/logo/cut.py (нужны Pillow и numpy).
import os

import numpy as np
from PIL import Image

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
src = np.asarray(Image.open(os.path.join(ROOT, "assets", "logo", "source.jpg")).convert("RGB")).astype(float)
ink = src.max(axis=2) > 60


def bbox(rows):
    """Рамка чернил в полосе строк rows: (x0, y0, x1, y1) включительно."""
    band = ink[rows[0]:rows[1] + 1]
    ys = np.where(band.any(axis=1))[0] + rows[0]
    xs = np.where(band.any(axis=0))[0]
    return xs.min(), ys.min(), xs.max(), ys.max()


# Иконка и надпись — две полосы строк, между ними пустая.
rows = np.where(ink.any(axis=1))[0]
gap = np.argmax(np.diff(rows) > 5)
icon_box = bbox((rows[0], rows[gap]))
text_box = bbox((rows[gap + 1], rows[-1]))


def crop(box, pad):
    x0, y0, x1, y1 = box
    return src[max(0, y0 - pad):y1 + 1 + pad, max(0, x0 - pad):x1 + 1 + pad]


def transparent(rgb):
    """Логотип нарисован на чёрном: яркость — это непрозрачность, цвет — rgb / непрозрачность."""
    a = rgb.max(axis=2) / 255.0
    a[a < 0.03] = 0
    color = np.where(a[..., None] > 0, rgb / np.maximum(a[..., None], 1e-6), 0)
    out = np.dstack([np.clip(color, 0, 255), a * 255]).astype(np.uint8)
    return Image.fromarray(out, "RGBA")


def by_height(img, h):
    w = round(img.width * h / img.height)
    return img.resize((w, h), Image.LANCZOS)


def square_on_black(box, margin):
    """Иконка по центру чёрного квадрата с полями margin (доля стороны)."""
    icon = Image.fromarray(crop(box, 0).astype(np.uint8), "RGB")
    side = round(max(icon.size) / (1 - 2 * margin))
    sq = Image.new("RGB", (side, side), (0, 0, 0))
    sq.paste(icon, ((side - icon.width) // 2, (side - icon.height) // 2))
    return sq


web = os.path.join(ROOT, "web", "static")
# Шапка: иконка 32 px и надпись 16 px высотой — с запасом в 3 раза для экранов высокой плотности.
by_height(transparent(crop(icon_box, 2)), 96).save(os.path.join(web, "logo-icon.png"), optimize=True)
by_height(transparent(crop(text_box, 2)), 48).save(os.path.join(web, "logo-text.png"), optimize=True)

sq = square_on_black(icon_box, 0.12)
sq.resize((192, 192), Image.LANCZOS).save(os.path.join(web, "icon-192.png"), optimize=True)
sq.save(os.path.join(web, "favicon.ico"), sizes=[(16, 16), (32, 32), (48, 48)])
sizes = [(16, 16), (24, 24), (32, 32), (48, 48), (64, 64), (128, 128), (256, 256)]
for dst in (os.path.join(ROOT, "installer", "kinodom.ico"), os.path.join(ROOT, "internal", "tray", "kinodom.ico")):
    os.makedirs(os.path.dirname(dst), exist_ok=True)
    sq.save(dst, sizes=sizes)
print("иконка", icon_box, "надпись", text_box)
