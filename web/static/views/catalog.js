// Каталог трекера: вкладки Rutracker и Rutor, раздел, сетка постеров по раздающим, страницы
// (спека этапа 7, разделы 5.4 и 6.3).
import { h, icon, ago, size, rating, store, plural, keepFocus, offWarn } from '../ui.js';
import { get } from '../api.js';

export const TRACKERS = [['rutracker', 'Rutracker'], ['rutor', 'Rutor']];

export function render(root, r, ctx) {
  const tracker = TRACKERS.some(([id]) => id === r.parts[1]) ? r.parts[1] : 'rutor';
  const section = r.parts[2] || '';
  const page = Math.max(1, parseInt(r.query.get('page') || '1', 10) || 1);
  let alive = true;

  const tabs = h('nav', { class: 'tabs', 'aria-label': 'Трекер' });
  const updated = h('div', { class: 'muted small' });
  const warn = h('div');
  const bar = h('nav', { class: 'filters', 'aria-label': 'Разделы' });
  const grid = h('div', { class: 'grid' });
  const pages = h('nav', { class: 'pages', 'aria-label': 'Страницы' });
  root.append(h('div', { class: 'screen' }, h('div', { class: 'row' }, tabs, h('div', { class: 'grow' }), updated), warn, bar, grid, pages));

  // Вкладки и предупреждение трекера — из «Состояния»: у вкладки со значком есть проблемы.
  const onStatus = (status) => {
    const trackers = (status && status.trackers) || {};
    keepFocus(tabs, () => tabs.replaceChildren(...TRACKERS.map(([id, title]) => {
      const bad = trackers[id] && trackers[id].state !== 'ok';
      return h('a', { href: `#/catalog/${id}`, class: id === tracker ? 'on' : null, 'aria-current': id === tracker ? 'page' : null, 'data-key': `tab-${id}` },
        title, bad ? icon('warning', 18, 'Есть проблемы') : null);
    })));
    const t = trackers[tracker];
    keepFocus(warn, () => warn.replaceChildren(t && t.state === 'off' ? offWarn(t.text)
      : t && t.state !== 'ok' && t.text ? h('div', { class: 'warn' }, icon('warning'), t.text) : ''));
  };
  ctx.listeners.add(onStatus);
  onStatus(ctx.status);

  const q = new URLSearchParams({ tracker, page: String(page) });
  if (section) q.set('section', section);
  Promise.all([get(`/catalog/sections?tracker=${tracker}`), get(`/catalog?${q}`)]).then(([sections, list]) => {
    if (!alive) return;
    updated.textContent = list.updatedAt ? `обновлён ${ago(list.updatedAt)}` : 'ещё не обновлялся';
    bar.replaceChildren(...sections.map((s) => h('a', {
      class: s.id === list.section ? 'fil on' : 'fil',
      href: `#/catalog/${tracker}/${encodeURIComponent(s.id)}`,
      'aria-current': s.id === list.section ? 'page' : null,
    }, s.name)));
    bar.querySelector('.on')?.scrollIntoView({ block: 'nearest', inline: 'nearest' });
    if (list.section) store.set('catalog', `#/catalog/${tracker}/${encodeURIComponent(list.section)}`);
    const off = ctx.status && ctx.status.trackers && ctx.status.trackers[tracker] && ctx.status.trackers[tracker].state === 'off';
    if (sections.length === 0) {
      // Трекер без адреса (этап 11a) не обновляется — об этом строка «Укажите адрес» выше.
      grid.replaceChildren(off ? '' : h('p', { class: 'muted' }, 'Каталог ещё пуст — идёт первое обновление'));
    } else if (list.entries.length === 0) {
      grid.replaceChildren(h('p', { class: 'muted' }, 'Здесь пусто'));
    } else {
      grid.replaceChildren(...list.entries.map(entry));
    }
    pages.replaceChildren(...pager(list.page, list.pages, (n) => `#/catalog/${tracker}/${encodeURIComponent(list.section)}?page=${n}`));
  }).catch((e) => {
    if (alive) grid.replaceChildren(h('p', { class: 'error' }, e.message));
  });

  return () => {
    alive = false;
    ctx.listeners.delete(onStatus);
  };
}

// entry — раздача в сетке: постер, название, год, качество и формат, раздающие и размер. Раздача, у
// которой ещё нет названия (не догружена), — заглушкой.
export function entry(e) {
  const title = e.name || e.title;
  return h('a', { class: 'entry', href: `#/release/${e.id}` },
    poster(e, title),
    e.title
      ? [h('div', { class: 'etitle' }, title), h('div', { class: 'muted small' }, [e.year || null, e.quality || null, e.format || null].filter(Boolean).join(' · '))]
      : h('div', { class: 'lines', 'aria-label': 'Название ещё не загружено' }, h('div', { class: 'skel', style: { width: '90%' } }), h('div', { class: 'skel', style: { width: '60%' } })),
    h('div', { class: 'stats' },
      h('span', { class: 'stat', title: 'Раздающих' }, icon('arrow_upward', 16), String(e.seeders)),
      h('span', { class: 'stat', title: 'Размер' }, size(e.size))));
}

// poster — картинка раздачи; нет картинки или она не загрузилась — тёмный прямоугольник с названием. У
// карточки фильма с несколькими раздачами — «N раздач» (спека этапа 7, раздел 10.7).
export function poster(e, title, cls = 'poster') {
  const box = h('div', { class: cls },
    e.kinopoisk > 0 ? h('span', { class: 'kp' }, 'КП ' + rating(e.kinopoisk)) : null,
    e.variants > 1 ? h('span', { class: 'vars' }, plural(e.variants, 'раздача', 'раздачи', 'раздач')) : null,
    h('div', { class: 'ptitle' }, title || ''));
  if (e.imageKey) {
    const img = h('img', { src: `/img/${e.imageKey}`, alt: '', loading: 'lazy', decoding: 'async' });
    img.addEventListener('load', () => box.classList.add('has-img'));
    img.addEventListener('error', () => img.remove());
    box.prepend(img);
  }
  return box;
}

// pager — пять номеров вокруг текущей страницы и стрелки.
export function pager(current, total, href) {
  if (total <= 1) return [];
  const from = Math.max(1, Math.min(current - 2, total - 4));
  const to = Math.min(total, from + 4);
  const out = [];
  if (current > 1) out.push(h('a', { class: 'page', href: href(current - 1), 'aria-label': 'Предыдущая страница' }, icon('chevron_left')));
  for (let n = from; n <= to; n++) {
    out.push(h('a', { class: n === current ? 'page on' : 'page', href: href(n), 'aria-current': n === current ? 'page' : null }, String(n)));
  }
  if (current < total) out.push(h('a', { class: 'page', href: href(current + 1), 'aria-label': 'Следующая страница' }, icon('chevron_right')));
  return out;
}
