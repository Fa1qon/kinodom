// «Новые серии» (спека 11b, 6.1): раздачи, за которыми следят, — что вышло; «Смотреть» — первая новая
// серия, «Убрать» — строка уходит. Общие для семьи; строка уходит и сама, когда серии досмотрели.
import { h, fill, icon, poll, keepFocus, day, openPlayer } from '../ui.js';
import { get, post, del } from '../api.js';
import { playerLink } from './release.js';

// updateLabel — «Холод — 1×07–1×08», «Холод — Раздача снята с трекера».
export function updateLabel(u) {
  return `${u.name || u.title} — ${u.label}`;
}

// bellText — число у колокольчика: пусто, когда новых серий нет.
export function bellText(n) {
  if (!n || n <= 0) return '';
  return n > 99 ? '99+' : String(n);
}

export function render(root, r, ctx) {
  let alive = true;
  let items = null;
  let error = '';
  let busy = 0; // строка, у которой идёт «Смотреть» или «Убрать»
  const list = h('div', { class: 'dl-list' });
  root.append(h('div', { class: 'screen' }, h('h1', null, 'Новые серии'), list));

  const listPoll = poll(async () => {
    try {
      items = await get('/updates');
      error = '';
    } catch (e) {
      error = e.message;
    }
    if (alive) draw();
  }, 15000);

  async function watch(u) {
    busy = u.id;
    draw();
    try {
      const res = await post(`/torrents/${u.hash}/files/${u.files[0].index}/watch`, {});
      if (ctx.local && res.launchUrl) openPlayer(res.launchUrl, res.m3uUrl, ctx.status && ctx.status.protocol);
      else location.href = playerLink(res);
    } catch (e) {
      error = e.message;
    }
    busy = 0;
    if (alive) draw();
  }

  async function dismiss(u) {
    busy = u.id;
    draw();
    try {
      await del(`/updates/${u.id}`);
    } catch (e) {
      error = e.message;
    }
    busy = 0;
    ctx.refreshStatus();
    listPoll.now();
  }

  function draw() {
    if (!items) {
      fill(list, error ? h('p', { class: 'error' }, error) : h('p', { class: 'muted' }, 'Загружается…'));
      return;
    }
    keepFocus(root, () => fill(list, error ? h('p', { class: 'error' }, error) : null,
      items.length ? items.map(row) : h('p', { class: 'empty' }, 'Новых серий нет')));
  }

  function row(u) {
    const thumb = h('div', { class: 'thumb' });
    if (u.imageKey) thumb.append(h('img', { src: `/img/${u.imageKey}`, alt: '', loading: 'lazy' }));
    const main = h('span', { class: 'dl-name' }, h('span', { class: 'strong ellipsis', title: u.title }, updateLabel(u)),
      h('span', { class: 'muted small' }, day(u.at)));
    return h('div', { class: 'dl' },
      h('a', { class: 'hist-link', href: `#/release/${u.releaseId}`, 'data-key': `open-${u.id}` }, thumb, main),
      h('div', { class: 'dl-actions' },
        u.kind === 'episodes' && u.files.length ? h('button', { class: 'btn', type: 'button', disabled: busy === u.id, 'data-key': `watch-${u.id}`,
          onclick: () => watch(u) }, icon('play_arrow'), 'Смотреть') : null,
        ctx.canEdit ? h('button', { class: 'btn', type: 'button', disabled: busy === u.id, 'data-key': `dismiss-${u.id}`,
          onclick: () => dismiss(u) }, icon('close'), 'Убрать') : null));
  }

  return () => {
    alive = false;
    listPoll.stop();
  };
}
