// «Настройки → Не распознано» (спека этапа 8, разделы 5.3 и 6.2): потоки, которые не привязались ни к
// одному каналу, — по названию; «Назначить» — поиск канала телепрограммы; «Скрыть». Страницы по 50.
import { h, fill, icon, keepFocus, plural } from '../ui.js';
import { get, put } from '../api.js';
import { layout, remoteNote } from './settings-layout.js';

export function render(root, r, ctx) {
  const q = r.query.get('q') || '';
  const page = Math.max(1, Number(r.query.get('page')) || 1);
  const content = layout(root, 'unrecognized', 'Не распознано');
  let alive = true;
  let data = null;
  let error = '';
  let open = ''; // название, для которого открыт поиск канала
  let found = [];

  const search = h('input', { class: 'input', name: 'q', value: q, placeholder: 'Название', 'aria-label': 'Название', 'data-key': 'q' });
  const pick = h('input', { class: 'input', name: 'channel', placeholder: 'Канал в телепрограмме', 'aria-label': 'Канал в телепрограмме', 'data-key': 'pick-q' });
  const list = h('div', { class: 'un-list' });
  const pages = h('div', { class: 'pages' });
  content.append(
    h('form', { class: 'row gap10', onsubmit: (e) => {
      e.preventDefault();
      ctx.go('#/settings/unrecognized' + (search.value.trim() ? '?q=' + encodeURIComponent(search.value.trim()) : ''));
    } }, h('div', { class: 'grow' }, search), h('button', { class: 'btn', type: 'submit', 'data-key': 'find' }, icon('search'), 'Найти')),
    list, pages);

  async function load() {
    try {
      data = await get(`/iptv/unrecognized?q=${encodeURIComponent(q)}&page=${page}`);
      error = '';
    } catch (e) {
      error = e.message;
    }
    if (alive) draw();
  }
  load();

  async function act(fn) {
    try {
      await fn();
      open = '';
      error = '';
    } catch (e) {
      error = e.message;
    }
    load();
  }

  function draw() {
    if (!data) {
      fill(list, error ? h('p', { class: 'error' }, error) : h('p', { class: 'muted' }, 'Загружается…'));
      return;
    }
    keepFocus(root, () => {
      const rows = data.items.map((u) => {
        const row = h('div', { class: 'un' },
          h('div', { class: 'row' },
            h('div', { class: 'grow un-name' }, h('span', { class: 'strong' }, u.sample),
              h('span', { class: 'muted small' }, [plural(u.streams, 'поток', 'потока', 'потоков'), `работают ${u.alive}`, u.playlists.join(', ')].join(' · '))),
            ctx.canEdit ? h('button', { class: 'btn', type: 'button', 'data-key': `assign-${u.name}`, onclick: () => {
              open = open === u.name ? '' : u.name;
              found = [];
              pick.value = u.sample;
              draw();
              if (open) pick.focus();
            } }, icon('swap_horiz'), 'Назначить') : null,
            ctx.canEdit ? h('button', { class: 'btn', type: 'button', 'data-key': `hide-${u.name}`, onclick: () => act(() => put('/iptv/names', { name: u.name, hidden: true })) },
              icon('visibility_off'), 'Скрыть') : null));
        if (open === u.name) {
          row.append(h('form', { class: 'row gap10', onsubmit: async (e) => {
            e.preventDefault();
            try {
              found = (await get(`/iptv/epg-channels?q=${encodeURIComponent(pick.value)}`)).items;
            } catch (err) {
              error = err.message;
            }
            draw();
            const first = list.querySelector('[data-key^="to-"]');
            if (first) first.focus();
          } }, h('div', { class: 'grow' }, pick), h('button', { class: 'btn', type: 'submit', 'data-key': 'pick-find' }, icon('search'), 'Найти')),
          h('div', { class: 'picks' }, found.map((c) => h('button', { class: 'btn', type: 'button', 'data-key': `to-${c.key}`,
            onclick: () => act(() => put('/iptv/names', { name: u.name, channel: c.key })) }, c.name))));
        }
        return row;
      });
      fill(list, ctx.canEdit ? '' : remoteNote(), error ? h('p', { class: 'error' }, error) : '',
        h('div', { class: 'muted' }, plural(data.total, 'название', 'названия', 'названий')),
        ...(rows.length ? rows : [h('p', { class: 'empty' }, 'Всё распознано')]));
      const link = (n) => `#/settings/unrecognized?${q ? 'q=' + encodeURIComponent(q) + '&' : ''}page=${n}`;
      fill(pages, ...(data.pages > 1 ? [
        page > 1 ? h('a', { class: 'page', href: link(page - 1), 'aria-label': 'Назад', 'data-key': 'prev' }, icon('chevron_left')) : null,
        h('span', { class: 'page on' }, String(page)),
        h('span', { class: 'muted' }, `из ${data.pages}`),
        page < data.pages ? h('a', { class: 'page', href: link(page + 1), 'aria-label': 'Вперёд', 'data-key': 'next' }, icon('chevron_right')) : null,
      ] : []));
    });
  }

  return () => {
    alive = false;
  };
}
