// Каталог «Кинопоиск» (план 14Г): популярные фильмы и сериалы сайта по разделам (русские и зарубежные,
// документальные) в четырёх порядках; карточка ведёт в поиск раздач по названию и году.
import { h, ago, rating, store, keepFocus, poll, thumb } from '../ui.js';
import { get } from '../api.js';
import { orderLinks, oneAtATime, retryDue, trackerItems, filterSummary, firstColumn, filterButton, openFilters, takeReopen,
  takeFocusGrid } from './catalog-parts.js';

// KP_ORDER, KP_SECTION — выбор в памяти браузера.
export const KP_ORDER = 'kpcat.order';
const KP_SECTION = 'kpcat.section';

const SERIES = ['TV_SERIES', 'MINI_SERIES', 'TV_SHOW'];


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

// kpEmpty — что сказать в пустом разделе: почему не удалась последняя попытка обновления или «идёт первое
// обновление» (ревью 14Г: раньше второе висело и при неудаче).
export function kpEmpty(section) {
  if (section && section.error) return { text: section.error, error: true };
  return { text: 'Каталог ещё пуст — идёт первое обновление', error: false };
}

// kpUpdatedAt — когда обновлён раздел sec (у каждого своё; ревью 14Г); не обновлялся — null.
export function kpUpdatedAt(c, sec) {
  const s = ((c && c.sections) || []).find((x) => x.id === sec);
  return s && s.updatedAt ? s.updatedAt : null;
}

// freshOnly — фильмы порции, которых ещё нет на экране (shown пополняется): снимок порядка мог устареть, и
// сервер начал новый — повтор не рисуется (Review Focus 1, 15В).
export function freshOnly(entries, shown) {
  return (entries || []).filter((f) => {
    if (shown.has(f.id)) return false;
    shown.add(f.id);
    return true;
  });
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
    const img = h('img', { src: cls.includes('big') ? f.poster : thumb(f.poster), alt: '', loading: 'lazy', decoding: 'async' });
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
  // Над сеткой — сводка «Кинопоиск · раздел · порядок» (OK — панель выбора слева) и «обновлён» (план 17А).
  const updated = h('div', { class: 'muted small' });
  const fbtn = filterButton('Кинопоиск', () => openPanel());
  const grid = h('div', { class: 'grid' });
  const tail = h('div', { class: 'grid-tail' });
  root.append(h('div', { class: 'screen' }, h('div', { class: 'row fbar' }, fbtn, h('div', { class: 'grow' }), updated), grid, tail));
  let cat = { sections: [], orders: [] }; // разделы и порядки «Кинопоиска» (/kpcat)

  // openPanel — панель слева: трекеры, разделы «Кинопоиска», его порядки.
  function openPanel() {
    const base = `#/catalog/kinopoisk/${encodeURIComponent(sec)}`;
    openFilters({
      trackers: trackerItems('kinopoisk', (ctx.status && ctx.status.trackers) || {}),
      sections: cat.sections.map((s) => ({ id: s.id, name: s.name, href: `#/catalog/kinopoisk/${encodeURIComponent(s.id)}`, on: s.id === sec })),
      orders: orderLinks(cat.orders, order, base).map((o) => ({ ...o, onPick: () => store.set(KP_ORDER, o.id) })),
    });
  }

  let sec = '';
  let offset = 0;
  let more = true;
  let busy = false;
  let failed = false; // последняя порция не пришла — «вниз» и прокрутка просят её снова (ревью 14Г)
  let snap = ''; // снимок порядка с сервера: порции листают его (ревью 14Г)
  const shown = new Set();
  let waiting = null; // опрос пустого раздела

  const load = oneAtATime(async () => {
    if (!alive || !more || busy || !sec) return;
    busy = true;
    tail.replaceChildren(h('div', { class: 'muted', 'aria-label': 'Загружается' }, '…'));
    try {
      const q = { section: sec, order, offset: String(offset) };
      if (snap && offset > 0) q.snap = snap;
      const v = await get(`/kpcat/list?${new URLSearchParams(q)}`);
      if (!alive) return;
      failed = false;
      snap = v.snap || '';
      grid.append(...freshOnly(v.entries, shown).map(card));
      offset += v.entries.length;
      more = v.more && v.entries.length > 0;
      tail.replaceChildren();
      remember();
      if (offset === 0) waitFilms();
    } catch (e) {
      failed = true;
      if (alive) tail.replaceChildren(h('p', { class: 'error' }, e.message));
    } finally {
      busy = false;
    }
    if (alive && more && !failed && tail.getBoundingClientRect().top < window.innerHeight + 600) setTimeout(() => load(), 0);
  });

  // updatedLine — «обновлён …» раздела (у каждого своё; ревью 14Г).
  function updatedLine(c) {
    const at = kpUpdatedAt(c, sec);
    updated.textContent = at ? `обновлён ${ago(at)}` : 'ещё не обновлялся';
  }

  // waitFilms — раздел пуст: правда о нём и опрос сервера, пока фильмы не появятся, — тогда экран рисуется сам
  // (ревью 14Г). На сайт опрос не ходит — только к своему серверу.
  function waitFilms() {
    if (waiting || !alive) return;
    waiting = poll(async () => {
      const c = await get('/kpcat');
      if (!alive) return;
      updatedLine(c);
      const s = c.sections.find((x) => x.id === sec);
      if (s && s.count > 0) {
        waiting.stop();
        waiting = null;
        grid.replaceChildren();
        offset = 0;
        more = true;
        snap = '';
        shown.clear();
        load();
        return;
      }
      const e = kpEmpty(s);
      grid.replaceChildren(h('p', { class: e.error ? 'error' : 'muted' }, e.text));
    }, 10000);
  }

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
  const inLastRow = (el) => {
    const last = grid.lastElementChild;
    return !!last && el.getBoundingClientRect().top >= last.getBoundingClientRect().top - 1;
  };
  grid.addEventListener('focusin', (e) => {
    if (inLastRow(e.target)) load();
    remember();
  });
  // «Вниз» из последнего ряда: идти некуда, а порция не пришла — попросить снова (пульт ТВ; ревью 14Г).
  grid.addEventListener('keydown', (e) => {
    if (e.key === 'ArrowDown' && inLastRow(e.target) && retryDue({ error: failed, loading: busy, more }, tail.getBoundingClientRect().top, window.innerHeight)) load();
    // «Влево» с первой колонки — панель выбора (план 17А).
    const first = grid.firstElementChild;
    if (e.key === 'ArrowLeft' && e.target.closest && e.target.closest('.entry') && first
      && firstColumn(e.target.getBoundingClientRect(), first.getBoundingClientRect())) {
      e.preventDefault();
      e.stopPropagation();
      openPanel();
    }
  });

  get('/kpcat').then(async (c) => {
    if (!alive) return;
    sec = c.sections.some((s) => s.id === section) ? section : (c.sections[0] || {}).id || '';
    updatedLine(c);
    store.set('catalog', `#/catalog/kinopoisk/${encodeURIComponent(sec)}`);
    store.set(KP_SECTION, sec);
    cat = c;
    const secName = (c.sections.find((s) => s.id === sec) || {}).name || '';
    const ordName = (c.orders.find((o) => o.id === order) || {}).name || '';
    const text = filterSummary(['Кинопоиск', secName, ordName]);
    fbtn.title = text;
    fbtn.querySelector('.btn-label').textContent = text;
    const reopen = takeReopen();
    const toGrid = takeFocusGrid();
    if (reopen) openPanel();
    if (watcher) watcher.observe(tail);
    await load();
    // Пришли из панели, выбрав раздел или порядок, — фокус на первую карточку.
    if (alive && toGrid && !reopen) {
      const firstCard = grid.querySelector('.entry');
      if (firstCard) firstCard.focus({ preventScroll: true });
    }
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
    if (waiting) waiting.stop();
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
