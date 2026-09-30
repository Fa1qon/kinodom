// Обзор папок (спека этапа 11a, раздел 7): диск → папка → «Выбрать». Окно поверх экрана; управление
// с пульта ТВ — стрелки и OK, «Назад» закрывает окно. Здесь же — «Разрешить доступ» к папке, которую
// служба не читает (раздел 4.7).
import { h, fill, icon, size } from '../ui.js';
import { get } from '../api.js';

// crumbs — части пути для строки пути: 'D:\Share\Movies' → D:, Share, Movies с полными путями.
export function crumbs(path) {
  const parts = String(path || '').split(/[\\/]+/).filter(Boolean);
  if (parts.length === 0) return [];
  const drive = parts[0].toUpperCase();
  const out = [{ name: drive, path: drive + '\\' }];
  let cur = drive;
  for (const p of parts.slice(1)) {
    cur += '\\' + p;
    out.push({ name: p, path: cur });
  }
  return out;
}

// grantView — «Разрешить доступ» у папки, которую служба не читает: на ПК с Kinodom — ссылка
// kinodom://grant (окно Windows «Да/Нет»), с другого устройства — строка; остальным папкам — ничего.
export function grantView(ctx, folder, port = Number(globalThis.location && globalThis.location.port) || 80) {
  if (!folder || folder.problem !== 'no_access') return null;
  if (!ctx.local) return { hint: 'Откройте пульт на ПК с Kinodom, чтобы разрешить доступ' };
  return { link: 'kinodom://grant?' + new URLSearchParams({ path: folder.path, port: String(port) }).toString() };
}

// grantControl — кнопка или строка «Разрешить доступ» для папки. onGrant — нажали: экран снова
// опрашивает сервер (итог окна Windows «Да/Нет» придёт позже).
export function grantControl(ctx, folder, key, onGrant) {
  const g = grantView(ctx, folder);
  if (!g) return null;
  if (g.hint) return h('span', { class: 'muted small' }, g.hint);
  return h('a', { class: 'btn', href: g.link, 'data-key': key, onclick: onGrant }, icon('lock_open'), 'Разрешить доступ');
}

// PROFILE — русские названия папок профиля (их предлагает сервер, когда профиль службе не виден).
const PROFILE = { Desktop: 'Рабочий стол', Downloads: 'Загрузки', Videos: 'Видео' };

// pickFolder — окно обзора папок; start — с какой папки начать ("" — со списка дисков). Возвращает
// выбранный путь или null (закрыли).
export function pickFolder(start = '') {
  return new Promise((resolve) => {
    const before = document.activeElement;
    const others = [...document.body.children];
    for (const el of others) el.inert = true; // стрелки пульта не уходят за окно
    const pathLine = h('div', { class: 'dlg-path' });
    const list = h('div', { class: 'list dlg-list' });
    const err = h('p', { class: 'error' });
    const choose = h('button', { class: 'btn inv', type: 'button', 'data-key': 'dlg-choose', onclick: () => close(current) }, icon('check'), 'Выбрать');
    const cancel = h('button', { class: 'btn', type: 'button', 'data-key': 'dlg-cancel', onclick: () => close(null) }, 'Отмена');
    const box = h('div', { class: 'dlg card', role: 'dialog', 'aria-modal': 'true', 'aria-label': 'Выбор папки' },
      h('div', { class: 'h' }, 'Выбор папки'), pathLine, err, list, h('div', { class: 'row gap10' }, choose, cancel));
    const back = h('div', { class: 'dlg-back', onkeydown: (e) => {
      const typing = e.target.tagName === 'INPUT';
      if (e.key === 'Escape' || e.key === 'GoBack' || e.key === 'BrowserBack' || (e.key === 'Backspace' && !typing)) {
        e.preventDefault(); // «Назад» пульта закрывает окно, а не уходит с экрана
        e.stopPropagation();
        close(null);
      }
    } }, box);
    document.body.append(back);
    let current = '';

    function close(result) {
      back.remove();
      for (const el of others) el.inert = false;
      if (before && before.focus) before.focus({ preventScroll: true });
      resolve(result);
    }

    async function open(path) {
      err.textContent = '';
      let v;
      try {
        v = await get('/fs/dirs' + (path ? '?path=' + encodeURIComponent(path) : ''));
      } catch (e) {
        err.textContent = e.message;
        if (path) return; // папка не открылась — остаёмся, где были
        v = { dirs: [], drives: [] };
      }
      current = v.path || '';
      choose.disabled = !current;
      fill(pathLine, current ? crumbs(current).map((c, i) => [i > 0 ? h('span', { class: 'muted' }, '\\') : null,
        h('button', { class: 'crumb', type: 'button', 'data-key': `crumb-${i}`, onclick: () => open(c.path) }, c.name)]) : h('span', { class: 'muted' }, 'Диски'));
      const rows = [];
      if (current) {
        rows.push(h('button', { class: 'dlg-row', type: 'button', 'data-key': 'dlg-up', onclick: () => open(v.parent) }, icon('arrow_upward'), 'Вверх'));
        if (v.denied) rows.push(h('div', { class: 'dlg-row muted' }, icon('warning'), 'Нет доступа'));
        for (const d of v.dirs) {
          rows.push(h('button', { class: 'dlg-row', type: 'button', 'data-key': `dir-${d}`, onclick: () => open(current.replace(/\\$/, '') + '\\' + d) },
            icon('folder'), v.denied && PROFILE[d] ? [PROFILE[d], h('span', { class: 'muted small' }, d)] : d));
        }
      } else {
        for (const d of v.drives || []) {
          rows.push(h('button', { class: 'dlg-row', type: 'button', 'data-key': `drive-${d.path}`, onclick: () => open(d.path) },
            icon('storage'), d.path, h('span', { class: 'muted small grow right' }, `свободно ${size(d.free)}`)));
        }
      }
      fill(list, rows);
      const first = list.querySelector('button') || choose;
      first.focus({ preventScroll: true });
    }
    open(start);
  });
}
