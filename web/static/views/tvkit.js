// Общее для экранов каналов (спека этапа 8, раздел 6): оценка проверки, время передач, «Смотреть»,
// избранное устройства, логотип.
import { h, icon, openPlayer } from '../ui.js';
import { get, put, del } from '../api.js';

// GRADE — оценка первого источника канала: цвет и подпись (цвет считает сервер).
export const GRADE = {
  green: { color: 'var(--green)', label: 'без остановок' },
  yellow: { color: 'var(--yellow)', label: 'впритык' },
  red: { color: 'var(--red)', label: 'будет тормозить' },
  unrated: { color: '', label: 'ещё не проверен' },
  black: { color: 'var(--faint)', label: 'не отвечает' },
  alive: { color: '', label: 'ещё не проверен' },
  hidden: { color: '', label: 'скрыт' }, // источник скрыт вручную и плееру не предлагается (Х32)
};

// gradeMark — квадрат оценки с подписью для экранного диктора.
export function gradeMark(grade) {
  const g = GRADE[grade] || GRADE.unrated;
  return h('span', { class: g.color ? 'grade' : 'grade none', style: g.color ? { background: g.color } : null, role: 'img', 'aria-label': g.label, title: g.label });
}

// inZone — момент как «часы на стене» в поясе UTC+offset: читать через getUTC…. Без offset — пояс
// устройства.
export function inZone(t, offset) {
  const ms = new Date(t).getTime();
  if (typeof offset !== 'number') return new Date(ms - new Date(ms).getTimezoneOffset() * 60000);
  return new Date(ms + offset * 3600000);
}

// hhmm — «19:30» по поясу каналов из настроек (utcOffset в ответах сервера): у заказчика на ПК
// московское время, а каналы — по UTC+7.
export function hhmm(iso, offset) {
  const d = inZone(iso, offset);
  return `${String(d.getUTCHours()).padStart(2, '0')}:${String(d.getUTCMinutes()).padStart(2, '0')}`;
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
    openPlayer(res.launchUrl, res.m3uUrl, ctx.status && ctx.status.protocol); // обработчика kinodom:// нет — скачается .m3u8
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

// toggleFavorite — ★: добавить канал в избранное этого устройства или убрать; остальное избранное
// (и каналы без живых источников в нём) сервер не трогает.
export async function toggleFavorite(isFavorite, key) {
  const path = `/iptv/favorites/${encodeURIComponent(key)}`;
  if (isFavorite) await del(path);
  else await put(path, {});
}

// starButton — ★ на странице канала (в списке каналов её нет — отзыв заказчика 2026-09-30).
export function starButton(on, onclick, key) {
  return h('button', { class: on ? 'btn big star on' : 'btn big star', type: 'button', 'data-key': key, 'aria-pressed': String(on),
    title: on ? 'Убрать из избранного' : null, onclick }, icon(on ? 'star' : 'star_border'), on ? 'В избранном' : 'В избранное');
}
