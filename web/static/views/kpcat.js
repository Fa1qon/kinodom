// Каталог «Кинопоиск» (план 14Г): популярные фильмы и сериалы сайта по разделам (русские и зарубежные,
// документальные) в четырёх порядках; карточка ведёт в поиск раздач по названию и году.
import { h, ago, rating, store, keepFocus } from '../ui.js';
import { get } from '../api.js';
import { orderLinks, oneAtATime, trackerTabs } from './catalog.js';

// KP_ORDER, KP_SECTION — выбор в памяти браузера.
export const KP_ORDER = 'kpcat.order';
const KP_SECTION = 'kpcat.section';

// filmSearchHref — поиск раздач фильма: название и год; kp — шапка фильма над результатами.
export function filmSearchHref(f) {
  const q = [f.title, f.year || null].filter(Boolean).join(' ');
  return `#/search?q=${encodeURIComponent(q)}&kp=${f.id}`;
}

// filmLine — строка карточки: год, оценки Кинопоиска и IMDb; пустые части пропускаются.
export function filmLine(f) {
  return [f.year || null, f.kinopoisk > 0 ? 'КП ' + rating(f.kinopoisk) : null, f.imdb > 0 ? 'IMDb ' + rating(Math.round(f.imdb * 10) / 10) : null]
    .filter(Boolean).join(' · ');
}

// filmPoster — постер фильма (сервер скачивает его в кэш по первому показу); нет — тёмный прямоугольник с названием.
export function filmPoster(f, cls = 'poster') {
  const box = h('div', { class: cls },
    f.kinopoisk > 0 ? h('span', { class: 'kp' }, 'КП ' + rating(f.kinopoisk)) : null,
    h('div', { class: 'ptitle' }, f.title || ''));
  if (f.poster) {
    const img = h('img', { src: f.poster, alt: '', loading: 'lazy', decoding: 'async' });
    img.addEventListener('load', () => box.classList.add('has-img'));
    img.addEventListener('error', () => img.remove());
    box.prepend(img);
  }
  return box;
}

function card(f) {
  return h('a', { class: 'entry', href: filmSearchHref(f), 'data-key': `kp-${f.id}` },
    filmPoster(f),
    h('div', { class: 'etitle' }, f.title),
    h('div', { class: 'muted small' }, filmLine(f)));
}

export function render(root, r, ctx) {
  let alive = true;
  const section = r.parts[2] || store.get(KP_SECTION) || '';
  const order = r.query.get('order') || store.get(KP_ORDER) || 'popular';
  const tabs = h('nav', { class: 'tabs', 'aria-label': 'Трекер' }, trackerTabs('kinopoisk'));
  const updated = h('div', { class: 'muted small' });
  const bar = h('nav', { class: 'filters', 'aria-label': 'Разделы' });
  const obar = h('nav', { class: 'filters orders', 'aria-label': 'Порядок' });
  const grid = h('div', { class: 'grid' });
  const tail = h('div', { class: 'grid-tail' });
  root.append(h('div', { class: 'screen' }, h('div', { class: 'row' }, tabs, h('div', { class: 'grow' }), updated), bar, obar, grid, tail));

  let sec = '';
  let offset = 0;
  let more = true;
  let busy = false;

  const load = oneAtATime(async () => {
    if (!alive || !more || busy || !sec) return;
    busy = true;
    tail.replaceChildren(h('div', { class: 'muted', 'aria-label': 'Загружается' }, '…'));
    try {
      const v = await get(`/kpcat/list?${new URLSearchParams({ section: sec, order, offset: String(offset) })}`);
      if (!alive) return;
      grid.append(...v.entries.map(card));
      offset += v.entries.length;
      more = v.more && v.entries.length > 0;
      tail.replaceChildren();
      if (offset === 0) grid.replaceChildren(h('p', { class: 'muted' }, 'Каталог ещё пуст — идёт первое обновление'));
    } catch (e) {
      if (alive) tail.replaceChildren(h('p', { class: 'error' }, e.message));
    } finally {
      busy = false;
    }
    if (alive && more && tail.getBoundingClientRect().top < window.innerHeight + 600) setTimeout(() => load(), 0);
  });

  const watcher = typeof IntersectionObserver === 'function'
    ? new IntersectionObserver((es) => {
      if (es.some((e) => e.isIntersecting)) load();
    }, { rootMargin: '600px 0px' })
    : null;
  const onScroll = () => {
    if (tail.getBoundingClientRect().top < window.innerHeight + 600) load();
  };
  window.addEventListener('scroll', onScroll, { passive: true });
  // Пульт ТВ: фокус в последнем ряду — следующая порция.
  grid.addEventListener('focusin', (e) => {
    const last = grid.lastElementChild;
    if (last && e.target.getBoundingClientRect().top >= last.getBoundingClientRect().top - 1) load();
  });

  get('/kpcat').then((c) => {
    if (!alive) return;
    sec = c.sections.some((s) => s.id === section) ? section : (c.sections[0] || {}).id || '';
    updated.textContent = c.updatedAt ? `обновлён ${ago(c.updatedAt)}` : 'ещё не обновлялся';
    store.set('catalog', `#/catalog/kinopoisk/${encodeURIComponent(sec)}`);
    store.set(KP_SECTION, sec);
    keepFocus(bar, () => bar.replaceChildren(...c.sections.map((s) => h('a', {
      class: s.id === sec ? 'fil on' : 'fil',
      href: `#/catalog/kinopoisk/${encodeURIComponent(s.id)}`,
      'aria-current': s.id === sec ? 'page' : null,
      'data-key': `sec-${s.id}`,
    }, s.name))));
    const base = `#/catalog/kinopoisk/${encodeURIComponent(sec)}`;
    obar.replaceChildren(...orderLinks(c.orders, order, base).map((o) => h('a', {
      class: o.on ? 'fil on' : 'fil',
      href: o.href,
      'aria-current': o.on ? 'true' : null,
      'data-key': `ord-${o.id}`,
      onclick: () => store.set(KP_ORDER, o.id),
    }, o.name)));
    bar.querySelector('.on')?.scrollIntoView({ block: 'nearest', inline: 'nearest' });
    if (watcher) watcher.observe(tail);
    load();
  }).catch((e) => {
    if (alive) grid.replaceChildren(h('p', { class: 'error' }, e.message));
  });

  return () => {
    alive = false;
    if (watcher) watcher.disconnect();
    window.removeEventListener('scroll', onScroll);
  };
}

// filmHeader — шапка фильма «Кинопоиска» над результатами поиска: постер, название, год, оценки, жанры.
export function filmHeader(f) {
  return h('div', { class: 'film-head' },
    filmPoster(f, 'poster small'),
    h('div', { class: 'col' },
      h('div', { class: 'strong' }, f.title),
      f.original && f.original !== f.title ? h('div', { class: 'muted small' }, f.original) : null,
      h('div', { class: 'muted small' }, filmLine(f)),
      f.genres && f.genres.length ? h('div', { class: 'muted small' }, f.genres.join(', ')) : null));
}
