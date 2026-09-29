// Общее для экранов каналов (спека этапа 8, раздел 6): оценка проверки, время передач, «Смотреть»,
// избранное устройства, логотип.
import { h, icon } from '../ui.js';
import { get, put } from '../api.js';

// GRADE — оценка первого источника канала: цвет и подпись (цвет считает сервер).
export const GRADE = {
  green: { color: 'var(--green)', label: 'без остановок' },
  yellow: { color: 'var(--yellow)', label: 'впритык' },
  red: { color: 'var(--red)', label: 'будет тормозить' },
  unrated: { color: '', label: 'ещё не проверен' },
  black: { color: 'var(--faint)', label: 'не отвечает' },
  alive: { color: '', label: 'ещё не проверен' },
};

// gradeMark — квадрат оценки с подписью для экранного диктора.
export function gradeMark(grade) {
  const g = GRADE[grade] || GRADE.unrated;
  return h('span', { class: g.color ? 'grade' : 'grade none', style: g.color ? { background: g.color } : null, role: 'img', 'aria-label': g.label, title: g.label });
}

// hhmm — «19:30» по местному времени устройства.
export function hhmm(iso) {
  const d = new Date(iso);
  return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`;
}

// progressOf — сколько процентов передачи прошло к моменту now.
export function progressOf(p, now = Date.now()) {
  const s = new Date(p.start).getTime();
  const e = new Date(p.stop).getTime();
  if (!(e > s)) return 0;
  return Math.max(0, Math.min(100, Math.round(((now - s) / (e - s)) * 100)));
}

// logo — логотип канала; нет — первая буква названия.
export function logo(c, cls = 'ch-logo') {
  const box = h('span', { class: cls, 'aria-hidden': 'true' }, (c.name || '?').slice(0, 1));
  if (c.logo) {
    const img = h('img', { src: c.logo, alt: '', loading: 'lazy', onerror: () => img.remove() });
    box.append(img);
  }
  return box;
}

// watchChannel — «Смотреть»: на этом ПК — плеер ссылкой kinodom://, на Android — VLC ссылкой intent:
// на .m3u8 канала, на других устройствах — .m3u8 (спека этапа 8, раздел 6.2).
export async function watchChannel(key, ctx) {
  const res = await get(`/channels/${encodeURIComponent(key)}/play`);
  if (ctx.local && res.launchUrl) {
    location.href = res.launchUrl;
    return;
  }
  location.href = channelPlayerLink(res);
}

export function channelPlayerLink(res) {
  if (!/Android/i.test(navigator.userAgent)) return res.m3uUrl;
  const u = new URL(res.m3uUrl);
  return `intent://${u.host}${u.pathname}#Intent;scheme=${u.protocol.replace(':', '')};type=audio/x-mpegurl;package=org.videolan.vlc;`
    + `S.title=${encodeURIComponent(res.title)};S.browser_fallback_url=${encodeURIComponent(res.m3uUrl)};end`;
}

// toggleFavorite — добавить канал в избранное этого устройства или убрать; favorites — текущий список.
export async function toggleFavorite(favorites, key) {
  const next = favorites.includes(key) ? favorites.filter((k) => k !== key) : [...favorites, key];
  await put('/iptv/favorites', { keys: next });
  return next;
}

// starButton — ★ в строке и карточке канала.
export function starButton(on, onclick, key) {
  return h('button', { class: on ? 'sq star on' : 'sq star', type: 'button', 'data-key': key, 'aria-pressed': String(on),
    'aria-label': on ? 'Убрать из избранного' : 'В избранное', onclick }, icon(on ? 'star' : 'star_border'));
}
