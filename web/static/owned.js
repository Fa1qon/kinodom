// Что уже есть в медиатеке — по фильму, а не по раздаче (просьба 2026-11-08: пять разных раздач одного сезона —
// всё равно «скачан»). Медиатека сама сводит раздачи в карточки по номеру Кинопоиска (ключ «kp-<номер>»); здесь —
// номер → ключ карточки, чтобы каталог, подборки и страница фильма отмечали скачанное и вели в медиатеку.
import { h, icon } from './ui.js';
import { get } from './api.js';

const FRESH_MS = 30000;
let cache = null; // { at, map: Map<kp, {key, downloading}> }
let loading = null;

// ownedMap — номер Кинопоиска → карточка медиатеки; не загрузилось — пустая карта (отметок просто не будет).
export async function ownedMap() {
  if (cache && Date.now() - cache.at < FRESH_MS) return cache.map;
  if (!loading) {
    loading = get('/library').then((v) => {
      const map = new Map();
      for (const c of v.cards || []) {
        const m = /^kp-(\d+)$/.exec(c.key || '');
        if (m) map.set(Number(m[1]), { key: c.key, downloading: !!c.downloading });
      }
      cache = { at: Date.now(), map };
      return map;
    }, () => new Map()).finally(() => { loading = null; });
  }
  return loading;
}

// forgetOwned — медиатека изменилась (скачали, удалили): следующий запрос перечитает её.
export function forgetOwned() {
  cache = null;
}

// haveBadge — метка «Есть» на постере.
export function haveBadge(downloading) {
  return h('span', { class: 'have', title: downloading ? 'Качается' : 'В медиатеке' }, icon(downloading ? 'download' : 'download_done', 15), downloading ? 'Качается' : 'Есть');
}

// markOwned — метки «Есть» на постерах внутри root, у которых data-kp — фильм из медиатеки.
export async function markOwned(root) {
  const map = await ownedMap();
  if (!map.size) return;
  for (const el of root.querySelectorAll('[data-kp]')) {
    const o = map.get(Number(el.dataset.kp));
    if (!o || el.querySelector(':scope > .have')) continue;
    el.append(haveBadge(o.downloading));
  }
}
