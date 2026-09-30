// Каталог трекера: вкладки Rutracker и Rutor, раздел, сетка постеров по раздающим, страницы
// (спека этапа 7, разделы 5.4 и 6.3).
import { h, icon, ago, size, rating, store, plural, keepFocus, offWarn } from '../ui.js';
import { get } from '../api.js';

export const TRACKERS = [['rutracker', 'Rutracker'], ['rutor', 'Rutor']];

// portions — подгрузка каталога порциями (замечание № 9 этапа 11b): {loaded, page, pages, loading, error}.
// 'more' — просить следующую (не во время загрузки и не после конца списка), 'loaded' — пришла,
// 'failed' — не пришла (можно попросить снова).
export function portions(state, action) {
  const s = state || { loaded: [], page: 0, pages: 1, loading: false, error: '' };
  switch (action.type) {
    case 'more':
      if (s.loading || s.page >= s.pages) return s;
      return { ...s, loading: true, error: '' };
    case 'loaded':
      return { loaded: [...s.loaded, ...action.list.entries], page: action.list.page, pages: action.list.pages, loading: false, error: '' };
    case 'failed':
      return { ...s, loading: false, error: action.error };
    default:
      return s;
  }
}

// oneAtATime — пока вызов fn идёт, повторный возвращает тот же промис: вторая просьба порции ждёт идущую
// загрузку, а не возвращается сразу (иначе экран считал список пустым — найдено вживую, 11b-А).
export function oneAtATime(fn) {
  let running = null;
  return () => {
    if (!running) running = Promise.resolve(fn()).finally(() => { running = null; });
    return running;
  };
}

export function render(root, r, ctx) {
  const tracker = TRACKERS.some(([id]) => id === r.parts[1]) ? r.parts[1] : 'rutor';
  const section = r.parts[2] || '';
  let alive = true;
  let state = portions(undefined, { type: 'init' });
  let shownSection = section;
  // Где были (замечание № 14): сколько порций, прокрутка и карточка в фокусе — в history.state этой
  // записи; пишется постоянно, пока каталог открыт (к моменту перехода на раздачу запись уже чужая).
  const here = location.hash.split('?')[0];
  const saved = history.state && history.state.catalog && history.state.catalog.at === here ? history.state.catalog : null;

  const tabs = h('nav', { class: 'tabs', 'aria-label': 'Трекер' });
  const updated = h('div', { class: 'muted small' });
  const warn = h('div');
  const bar = h('nav', { class: 'filters', 'aria-label': 'Разделы' });
  const grid = h('div', { class: 'grid' });
  const tail = h('div', { class: 'grid-tail' });
  root.append(h('div', { class: 'screen' }, h('div', { class: 'row' }, tabs, h('div', { class: 'grow' }), updated), warn, bar, grid, tail));

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

  function remember() {
    if (!alive) return;
    const a = document.activeElement;
    const focusKey = a && grid.contains(a) && a.dataset ? a.dataset.key || '' : '';
    const st = { ...(history.state || {}), catalog: { at: here, pages: state.page, scrollY: window.scrollY, focusKey } };
    try {
      history.replaceState(st, '');
    } catch {
      // браузер не дал записать — место просто не запомнится
    }
  }

  // more — следующая порция, если её можно просить.
  const more = oneAtATime(loadMore);
  async function loadMore() {
    const next = portions(state, { type: 'more' });
    if (next === state) return;
    state = next;
    drawTail();
    const q = new URLSearchParams({ tracker, page: String(state.page + 1) });
    if (shownSection) q.set('section', shownSection);
    let list;
    try {
      list = await get(`/catalog?${q}`);
    } catch (e) {
      if (!alive) return;
      state = portions(state, { type: 'failed', error: e.message });
      drawTail();
      return;
    }
    if (!alive) return;
    shownSection = list.section;
    if (state.page === 0) updated.textContent = list.updatedAt ? `обновлён ${ago(list.updatedAt)}` : 'ещё не обновлялся';
    const was = state.loaded.length;
    state = portions(state, { type: 'loaded', list });
    grid.append(...state.loaded.slice(was).map(entry));
    drawTail();
    remember();
  }

  function drawTail() {
    tail.replaceChildren(
      state.error ? h('p', { class: 'error' }, state.error) : '',
      state.loading ? h('div', { class: 'muted', 'aria-label': 'Загружается' }, '…') : '');
  }

  // Следующая порция — когда низ сетки виден (мышь, касание) или фокус пульта пришёл в последний ряд.
  const watcher = typeof IntersectionObserver === 'function'
    ? new IntersectionObserver((es) => {
      if (es.some((e) => e.isIntersecting)) more();
    }, { rootMargin: '600px 0px' })
    : null;
  grid.addEventListener('focusin', (e) => {
    const last = grid.lastElementChild;
    if (last && e.target.closest && e.target.closest('.entry') && e.target.getBoundingClientRect().top >= last.getBoundingClientRect().top - 1) more();
    remember();
  });
  let scrollTimer = 0;
  const onScroll = () => {
    clearTimeout(scrollTimer);
    scrollTimer = setTimeout(remember, 200);
  };
  window.addEventListener('scroll', onScroll, { passive: true });

  get(`/catalog/sections?tracker=${tracker}`).then(async (sections) => {
    if (!alive) return;
    await more(); // первая порция: в ней — раздел по умолчанию и время обновления
    if (!alive) return;
    if (watcher) watcher.observe(tail); // только теперь: пустая сетка не должна просить порцию сама
    const off = ctx.status && ctx.status.trackers && ctx.status.trackers[tracker] && ctx.status.trackers[tracker].state === 'off';
    bar.replaceChildren(...sections.map((s) => h('a', {
      class: s.id === shownSection ? 'fil on' : 'fil',
      href: `#/catalog/${tracker}/${encodeURIComponent(s.id)}`,
      'aria-current': s.id === shownSection ? 'page' : null,
      'data-key': `sec-${s.id}`,
    }, s.name)));
    bar.querySelector('.on')?.scrollIntoView({ block: 'nearest', inline: 'nearest' });
    if (shownSection) store.set('catalog', `#/catalog/${tracker}/${encodeURIComponent(shownSection)}`);
    if (sections.length === 0) {
      // Трекер без адреса (этап 11a) не обновляется — об этом строка «Укажите адрес» выше.
      grid.replaceChildren(off ? '' : h('p', { class: 'muted' }, 'Каталог ещё пуст — идёт первое обновление'));
      return;
    }
    if (state.loaded.length === 0 && !state.error) {
      grid.replaceChildren(h('p', { class: 'muted' }, 'Здесь пусто'));
      return;
    }
    // Возврат со страницы раздачи — столько же порций, то же место и та же карточка.
    if (saved) {
      while (alive && state.page < saved.pages && !state.error) await more();
      if (!alive) return;
      window.scrollTo(0, saved.scrollY || 0);
      const el = saved.focusKey ? grid.querySelector(`[data-key="${CSS.escape(saved.focusKey)}"]`) : null;
      if (el) el.focus({ preventScroll: true });
    }
  }).catch((e) => {
    if (alive) grid.replaceChildren(h('p', { class: 'error' }, e.message));
  });

  return () => {
    alive = false;
    ctx.listeners.delete(onStatus);
    if (watcher) watcher.disconnect();
    window.removeEventListener('scroll', onScroll);
    clearTimeout(scrollTimer);
  };
}

// entry — раздача в сетке: постер, название, год, качество и формат, раздающие и размер. Раздача, у
// которой ещё нет названия (не догружена), — заглушкой.
export function entry(e) {
  const title = e.name || e.title;
  return h('a', { class: 'entry', href: `#/release/${e.id}`, 'data-key': `e-${e.id}` },
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
