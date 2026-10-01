// Каталог «Кинопоиск» (план 14Г): популярные фильмы и сериалы сайта по разделам (русские и зарубежные,
// документальные) в четырёх порядках; карточка ведёт в поиск раздач по названию и году.
import { h, ago, rating, store, keepFocus } from '../ui.js';
import { get } from '../api.js';
import { orderLinks, oneAtATime, trackerTabs } from './catalog.js';

// KP_ORDER, KP_SECTION — выбор в памяти браузера.
export const KP_ORDER = 'kpcat.order';
const KP_SECTION = 'kpcat.section';

const SERIES = ['TV_SERIES', 'MINI_SERIES', 'TV_SHOW'];

// kpFocusOrder — порядок выбрали с пульта ТВ: после перехода фокус — снова на ряд порядков (ревью 14Г).
let kpFocusOrder = false;

// filmSearchHref — поиск раздач фильма: название и год; у сериала — без года: идущий сезон новее первого, а
// трекерам нужны все слова запроса (ревью 14Г). kp — шапка фильма над результатами.
export function filmSearchHref(f) {
  const q = [f.title, SERIES.includes(f.type) ? null : f.year || null].filter(Boolean).join(' ');
  return `#/search?q=${encodeURIComponent(q)}&kp=${f.id}`;
}

// kpRestoreCount — возврат на место (ревью 14Г, как у трекеров): запись этой страницы — сколько карточек
// догрузить; чужая или нет — 0.
export function kpRestoreCount(saved, here) {
  return saved && saved.at === here ? Math.max(0, Number(saved.count) || 0) : 0;
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
  const here = location.hash;
  const saved = history.state && history.state.kpcat;
  const restore = kpRestoreCount(saved, here);
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
      remember();
      if (offset === 0) grid.replaceChildren(h('p', { class: 'muted' }, 'Каталог ещё пуст — идёт первое обновление'));
    } catch (e) {
      if (alive) tail.replaceChildren(h('p', { class: 'error' }, e.message));
    } finally {
      busy = false;
    }
    if (alive && more && tail.getBoundingClientRect().top < window.innerHeight + 600) setTimeout(() => load(), 0);
  });

  // Где были: число карточек, прокрутка и карточка в фокусе — в history.state этой записи (как у трекеров).
  function remember() {
    if (!alive) return;
    const a = document.activeElement;
    const focusKey = a && grid.contains(a) && a.dataset ? a.dataset.key || '' : '';
    try {
      history.replaceState({ ...(history.state || {}), kpcat: { at: here, count: offset, scrollY: window.scrollY, focusKey } }, '');
    } catch {
      // браузер не дал записать — место просто не запомнится
    }
  }

  const watcher = typeof IntersectionObserver === 'function'
    ? new IntersectionObserver((es) => {
      if (es.some((e) => e.isIntersecting)) load();
    }, { rootMargin: '600px 0px' })
    : null;
  let scrollTimer = 0;
  const onScroll = () => {
    if (tail.getBoundingClientRect().top < window.innerHeight + 600) load();
    clearTimeout(scrollTimer);
    scrollTimer = setTimeout(remember, 200);
  };
  window.addEventListener('scroll', onScroll, { passive: true });
  // Пульт ТВ: фокус в последнем ряду — следующая порция.
  grid.addEventListener('focusin', (e) => {
    const last = grid.lastElementChild;
    if (last && e.target.getBoundingClientRect().top >= last.getBoundingClientRect().top - 1) load();
    remember();
  });

  get('/kpcat').then(async (c) => {
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
      onclick: () => {
        store.set(KP_ORDER, o.id);
        kpFocusOrder = true;
      },
    }, o.name)));
    bar.querySelector('.on')?.scrollIntoView({ block: 'nearest', inline: 'nearest' });
    const on = obar.querySelector('.on');
    if (kpFocusOrder && on) on.focus({ preventScroll: true });
    kpFocusOrder = false;
    if (watcher) watcher.observe(tail);
    await load();
    // Возврат с поиска фильма — столько же карточек, то же место и та же карточка.
    for (let i = 0; i < 40 && alive && more && offset < restore; i++) {
      const was = offset;
      await load();
      if (offset === was) break;
    }
    if (alive && restore && saved) {
      window.scrollTo(0, saved.scrollY || 0);
      const el = saved.focusKey ? grid.querySelector(`[data-key="${CSS.escape(saved.focusKey)}"]`) : null;
      if (el) el.focus({ preventScroll: true });
    }
  }).catch((e) => {
    if (alive) grid.replaceChildren(h('p', { class: 'error' }, e.message));
  });

  return () => {
    alive = false;
    if (watcher) watcher.disconnect();
    window.removeEventListener('scroll', onScroll);
    clearTimeout(scrollTimer);
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
