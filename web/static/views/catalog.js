// Каталог трекера: вкладки Rutracker и Rutor, раздел, сетка постеров по раздающим, страницы
// (спека этапа 7, разделы 5.4 и 6.3).
import { h, icon, ago, size, rating, store, plural, keepFocus, offWarn, poll, formatTag, altFormatTag } from '../ui.js';
import { get } from '../api.js';
import { render as renderKP } from './kpcat.js';

export const TRACKERS = [['rutracker', 'Rutracker'], ['rutor', 'Rutor']];

// portions — подгрузка каталога порциями (замечание № 9 этапа 11b): {loaded, page, next, more, loading,
// error}; page — сколько порций пришло, next — курсор (место последней карточки раздела, -1 — с начала;
// ревью 11b-Г). 'more' — просить следующую (не во время загрузки и не после конца списка), 'loaded' —
// пришла, 'failed' — не пришла (можно попросить снова, с того же места).
export function portions(state, action) {
  const s = state || { loaded: [], page: 0, next: -1, more: true, loading: false, error: '' };
  switch (action.type) {
    case 'more':
      if (s.loading || !s.more) return s;
      return { ...s, loading: true, error: '' };
    case 'loaded':
      return { loaded: [...s.loaded, ...action.list.entries], page: s.page + 1, next: action.list.next, more: action.list.more,
        loading: false, error: '' };
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

// restoreDepth — возврат со страницы раздачи: догрузить столько порций, сколько было (pages), но не
// больше, чем есть у сервера сейчас, и не просить снова порцию, которая не пришла — каталог мог стать
// короче (склейка карточек, новый топ), иначе цикл без конца вешает пульт (ревью 11b-А).
export async function restoreDepth(more, getState, pages) {
  for (;;) {
    const s = getState();
    if (s.error || !s.more || s.page >= pages) return;
    await more();
    if (getState().page === s.page) return;
  }
}

// retryDue — порция не пришла, а низ сетки на экране или рядом: прокрутка или «вниз» просят её снова
// (наблюдатель пересечения второй раз не срабатывает, пока низ не ушёл из зоны — ревью 11b-А).
export function retryDue(state, tailTop, viewportH) {
  return !!state.error && !state.loading && state.more && tailTop < viewportH + 600;
}

// fillDue — порция пришла, а низ сетки всё ещё рядом: следующую — сразу, не дожидаясь наблюдателя
// (он срабатывает только на вход в зону; вживую 11b-Г низ оставался в зоне, и подгрузка вставала).
export function fillDue(state, tailTop, viewportH) {
  return !state.error && !state.loading && state.more && tailTop < viewportH + 600;
}

// POSTER_TRIES — сколько раз ещё спросить карточку, у которой страница уже есть, а постера нет (опрос раз
// в 3 с — около минуты): постер приходит через секунды после страницы, не пришёл — хостинг мёртв.
export const POSTER_TRIES = 20;

// waiting — карточку стоит спрашивать (спека 11b, 14.1): страницы раздачи ещё нет или постера нет, а
// попытки (tries — опросов со страницей) не кончились.
export function waiting(e, tries) {
  return !!e.detailsPending || (!e.imageKey && tries < POSTER_TRIES);
}

// nearest — номера карточек, которые спросить сейчас: на экране и рядом (экран вверх, два вниз), ближние
// к экрану первыми, не больше limit. cards — [{id, top, bottom}] по getBoundingClientRect.
export function nearest(cards, viewportH, limit) {
  const dist = (c) => (c.bottom < 0 ? -c.bottom : c.top > viewportH ? c.top - viewportH : 0);
  return cards.filter((c) => c.bottom > -viewportH && c.top < 2 * viewportH)
    .sort((a, b) => dist(a) - dist(b) || a.top - b.top)
    .slice(0, limit)
    .map((c) => c.id);
}

// SHOWN — поля карточки, которые видно: изменилось одно — карточка перерисовывается.
const SHOWN = ['title', 'name', 'original', 'year', 'quality', 'format', 'season', 'imageKey', 'kinopoisk', 'seeders', 'size', 'detailsPending'];

// changed — у карточки изменилось видимое.
export function changed(a, b) {
  return SHOWN.some((k) => a[k] !== b[k]);
}

// ASK_AT_ONCE — сколько карточек у экрана спрашивать за раз.
const ASK_AT_ONCE = 48;

// PLACE — место раздела (порции, прокрутка, карточка в фокусе) во вкладке браузера: ссылка «назад» со
// страницы раздачи, открытой не прямо из каталога, возвращает на него (спека 11b, 14.3).
const PLACE = 'kinodom.catalogPlace';

function keepPlace(place) {
  try {
    sessionStorage.setItem(PLACE, JSON.stringify(place));
  } catch {
    // вкладка не дала записать — ссылка «назад» откроет раздел сверху
  }
}

function lastPlace() {
  try {
    return JSON.parse(sessionStorage.getItem(PLACE) || 'null');
  } catch {
    return null;
  }
}

// backStep — «назад» ссылкой со страницы раздачи в раздел href (prev — адрес до раздачи, place — место во
// вкладке). Пришли прямо из этого раздела — шаг назад по истории: место — в её записи, как у кнопки «назад»
// браузера и Esc пульта ТВ. Иначе — переход, а место — запомненное, если оно этого раздела.
export function backStep(href, prev, place) {
  const mine = place && place.key === href ? place : null;
  if (prev && (prev === href || (mine && prev === mine.at))) return { back: true, place: null };
  return { back: false, place: mine ? { ...mine, at: href } : null };
}

// returnTo — «назад» в раздел каталога href со страницы раздачи.
export function returnTo(href, prev) {
  const step = backStep(href, prev, lastPlace());
  if (step.back) {
    history.back();
    return;
  }
  location.hash = href;
  if (!step.place) return;
  try {
    history.replaceState({ ...(history.state || {}), catalog: step.place }, ''); // новая запись — с местом раздела
  } catch {
    // браузер не дал записать — раздел откроется сверху
  }
}

// groupBar — ряды над сеткой (спека 11b, 7.1): у Rutracker — группы («Кино · Сериалы · Документалистика»,
// только где что-то выбрано; выбранная — по разделу, ссылка — на первый её подраздел) и подразделы
// выбранной группы; раздел без группы — в ряду всегда. У Rutor групп нет — один ряд, как раньше.
export function groupBar(sections, current) {
  const groups = [];
  for (const s of sections) {
    if (s.group && !groups.some((g) => g.id === s.group)) groups.push({ id: s.group, name: s.groupName, first: s.id, on: false });
  }
  const cur = sections.find((s) => s.id === current);
  const on = cur && cur.group ? cur.group : groups.length > 0 && !(cur && !cur.group) ? groups[0].id : '';
  for (const g of groups) g.on = g.id === on;
  return { groups, sections: sections.filter((s) => !s.group || s.group === on) };
}

// dotted — части строки через « · » (строки и элементы), пустые пропускаются.
export function dotted(parts) {
  const out = [];
  for (const p of parts.filter(Boolean)) {
    if (out.length) out.push(' · ');
    out.push(typeof p === 'number' ? String(p) : p);
  }
  return out;
}

// ORDER — выбранный порядок разделов в памяти браузера (план 14Б): {o, d} — порядок и умолчание «Параметров»,
// при котором его выбрали; умолчание сменили — сервер память не слушает (ревью 14Б). Нет — умолчание.
export const ORDER = 'catalog.order';

// focusOrderNext — порядок выбрали с пульта ТВ: после перехода фокус — снова на ряд порядков (ревью 14Б).
let focusOrderNext = false;

// readOrderMemory — память выбора: {o, d}; прежняя память — просто строка порядка.
export function readOrderMemory(raw) {
  if (!raw) return { o: '', d: '' };
  try {
    const m = JSON.parse(raw);
    if (m && typeof m.o === 'string') return { o: m.o, d: typeof m.d === 'string' ? m.d : '' };
  } catch {
    // прежняя память — строка
  }
  return { o: String(raw), d: '' };
}

// orderParams — порядок в запросе порции: показанный (следующие порции — тем же, что первая), иначе из
// адреса, иначе из памяти (с умолчанием, при котором выбран), иначе — умолчание сервера.
export function orderParams(urlOrder, mem, shown) {
  if (shown) return { order: shown };
  if (urlOrder) return { order: urlOrder };
  if (mem && mem.o) return mem.d ? { order: mem.o, since: mem.d } : { order: mem.o };
  return {};
}

// orderLinks — переключатель порядка над разделом: ссылки на тот же раздел в каждом порядке.
export function orderLinks(orders, current, base) {
  return (orders || []).map((o) => ({ id: o.id, name: o.name, on: o.id === current, href: `${base}?order=${o.id}` }));
}

// groupDigits — число с неразрывными пробелами между разрядами: «23 992».
function groupDigits(n) {
  return String(n).replace(/\B(?=(\d{3})+(?!\d))/g, '\u00a0');
}

// orderStat — цифра порядка на карточке (раздающие и размер там всегда): качающие, дата добавления, число
// скачиваний; нет цифры — null.
export function orderStat(e, order) {
  if (order === 'leechers' && e.leechers !== undefined) return { icon: 'arrow_downward', title: 'Качающих', text: String(e.leechers) };
  if (order === 'downloads' && e.downloads > 0) return { icon: 'download', title: 'Скачиваний', text: groupDigits(e.downloads) };
  if (order === 'new' && e.added) {
    const d = new Date(e.added);
    if (!Number.isNaN(d.getTime())) {
      const two = (n) => String(n).padStart(2, '0');
      return { icon: 'schedule', title: 'Добавлена', text: `${two(d.getDate())}.${two(d.getMonth() + 1)}` };
    }
  }
  return null;
}

// trackerTabs — вкладки каталога: трекеры (со значком, если у трекера проблемы) и «Кинопоиск» (план 14Г).
export function trackerTabs(current, trackers = {}) {
  return [...TRACKERS, ['kinopoisk', 'Кинопоиск']].map(([id, title]) => {
    const bad = trackers[id] && trackers[id].state !== 'ok';
    return h('a', { href: `#/catalog/${id}`, class: id === current ? 'on' : null, 'aria-current': id === current ? 'page' : null, 'data-key': `tab-${id}` },
      title, bad ? icon('warning', 18, 'Есть проблемы') : null);
  });
}

export function render(root, r, ctx) {
  if (r.parts[1] === 'kinopoisk') return renderKP(root, r, ctx);
  const tracker = TRACKERS.some(([id]) => id === r.parts[1]) ? r.parts[1] : 'rutor';
  const section = r.parts[2] || '';
  // Порядок: из адреса, иначе выбранный раньше, иначе — умолчание сервера (план 14Б).
  const urlOrder = r.query.get('order') || '';
  const mem = readOrderMemory(store.get(ORDER));
  let shownOrder = urlOrder || mem.o;
  let firstOrder = ''; // порядок первой пришедшей порции — им просятся следующие
  let defaultOrder = '';
  let alive = true;
  let state = portions(undefined, { type: 'init' });
  let shownSection = section;
  // Где были (замечание № 14): сколько порций, прокрутка и карточка в фокусе — в history.state этой
  // записи; пишется постоянно, пока каталог открыт (к моменту перехода на раздачу запись уже чужая).
  const here = location.hash.split('?')[0];
  const saved = history.state && history.state.catalog && history.state.catalog.at === here ? history.state.catalog : null;
  // Незаконченные карточки (без страницы или постера; спека 11b, 14.1): номер → {el, e, tries}.
  const live = new Map();

  const tabs = h('nav', { class: 'tabs', 'aria-label': 'Трекер' });
  const updated = h('div', { class: 'muted small' });
  const warn = h('div');
  const bar = h('nav', { class: 'filters', 'aria-label': 'Разделы' });
  const obar = h('nav', { class: 'filters orders', 'aria-label': 'Порядок' });
  const owarn = h('div');
  const grid = h('div', { class: 'grid' });
  const tail = h('div', { class: 'grid-tail' });
  root.append(h('div', { class: 'screen' }, h('div', { class: 'row' }, tabs, h('div', { class: 'grow' }), updated), warn, bar, obar, owarn, grid, tail));

  // Вкладки и предупреждение трекера — из «Состояния»: у вкладки со значком есть проблемы.
  const onStatus = (status) => {
    const trackers = (status && status.trackers) || {};
    keepFocus(tabs, () => tabs.replaceChildren(...trackerTabs(tracker, trackers)));
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
    // key — адрес показанного раздела, как у ссылки «назад» со страницы раздачи (раздел мог прийти по умолчанию).
    const key = shownSection ? `#/catalog/${tracker}/${encodeURIComponent(shownSection)}` : here;
    const place = { at: here, key, pages: state.page, scrollY: window.scrollY, focusKey };
    try {
      history.replaceState({ ...(history.state || {}), catalog: place }, '');
    } catch {
      // браузер не дал записать — место просто не запомнится
    }
    keepPlace(place);
  }

  // more — следующая порция, если её можно просить.
  const more = oneAtATime(loadMore);
  async function loadMore() {
    const next = portions(state, { type: 'more' });
    if (next === state) return;
    state = next;
    drawTail();
    const q = new URLSearchParams({ tracker, after: String(state.next) });
    if (shownSection) q.set('section', shownSection);
    for (const [k, v] of Object.entries(orderParams(urlOrder, mem, firstOrder))) q.set(k, v);
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
    if (state.page === 0) {
      updated.textContent = list.updatedAt ? `обновлён ${ago(list.updatedAt)}` : 'ещё не обновлялся';
      shownOrder = firstOrder = list.order || '';
      defaultOrder = list.defaultOrder || '';
      owarn.replaceChildren(list.orderError ? h('div', { class: 'warn' }, icon('warning'), list.orderError) : '');
      drawOrders(list.orders);
    }
    const was = state.loaded.length;
    state = portions(state, { type: 'loaded', list });
    grid.append(...state.loaded.slice(was).map(card));
    drawTail();
    remember();
    setTimeout(() => {
      if (alive && fillDue(state, tail.getBoundingClientRect().top, window.innerHeight)) more();
    }, 0);
  }

  // drawOrders — ряд порядков над сеткой (у раздела, где их больше одного); выбор запоминается.
  function drawOrders(orders) {
    if (!shownSection || !orders || orders.length < 2) {
      obar.replaceChildren();
      return;
    }
    const base = `#/catalog/${tracker}/${encodeURIComponent(shownSection)}`;
    obar.replaceChildren(...orderLinks(orders, shownOrder, base).map((o) => h('a', {
      class: o.on ? 'fil on' : 'fil',
      href: o.href,
      'aria-current': o.on ? 'true' : null,
      'data-key': `ord-${o.id}`,
      onclick: () => {
        store.set(ORDER, JSON.stringify({ o: o.id, d: defaultOrder }));
        focusOrderNext = true;
      },
    }, o.name)));
    const on = obar.querySelector('.on');
    if (on) on.scrollIntoView({ block: 'nearest', inline: 'nearest' });
    if (focusOrderNext && on) on.focus({ preventScroll: true });
    focusOrderNext = false;
  }

  // card — карточка порции; незаконченная — под присмотром опроса.
  function card(e) {
    const el = entry(e, shownOrder);
    if (waiting(e, 0)) live.set(e.id, { el, e, tries: 0 });
    return el;
  }

  // Раз в 3 с — незаконченные карточки у экрана: сервер догрузил название, постер, рейтинг — карточка
  // перерисовывается на месте, фокус пульта ТВ и метка «N раздач» остаются.
  const refresh = poll(async () => {
    if (!alive || live.size === 0) return;
    const ids = nearest([...live.values()].map((c) => {
      const b = c.el.getBoundingClientRect();
      return { id: c.e.id, top: b.top, bottom: b.bottom };
    }), window.innerHeight, ASK_AT_ONCE);
    if (ids.length === 0) return;
    const res = await get('/catalog/cards?ids=' + ids.join(','));
    if (!alive) return;
    const fresh = new Map(res.entries.map((e) => [e.id, e]));
    keepFocus(grid, () => {
      for (const id of ids) {
        const c = live.get(id);
        const f = fresh.get(id);
        if (!c) continue;
        if (!f) {
          live.delete(id); // раздача ушла с трекера — спрашивать нечего
          continue;
        }
        const e = { ...f, variants: c.e.variants };
        if (changed(c.e, e)) {
          const el = entry(e, shownOrder);
          c.el.replaceWith(el);
          c.el = el;
        }
        c.e = e;
        if (!e.detailsPending) c.tries++;
        if (!waiting(e, c.tries)) live.delete(id);
      }
    });
  }, 3000);

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
  const inLastRow = (el) => {
    const last = grid.lastElementChild;
    return !!last && !!el.closest && !!el.closest('.entry') && el.getBoundingClientRect().top >= last.getBoundingClientRect().top - 1;
  };
  grid.addEventListener('focusin', (e) => {
    if (inLastRow(e.target)) more();
    remember();
  });
  // «Вниз» из последнего ряда: идти некуда, а порция не пришла — попросить снова (пульт ТВ).
  grid.addEventListener('keydown', (e) => {
    if (e.key === 'ArrowDown' && inLastRow(e.target) && retryDue(state, tail.getBoundingClientRect().top, window.innerHeight)) more();
  });
  let scrollTimer = 0;
  // Прокрутка сама проверяет, близко ли низ: наблюдатель пересечения сообщает только смену «в зоне / вне
  // зоны» и вживую пропускал уход низа из зоны после порции — подгрузка вставала (11b-Г).
  const onScroll = () => {
    const tailTop = tail.getBoundingClientRect().top;
    if (retryDue(state, tailTop, window.innerHeight) || fillDue(state, tailTop, window.innerHeight)) more();
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
    const gb = groupBar(sections, shownSection);
    if (gb.groups.length > 0) {
      const groups = h('nav', { class: 'filters', 'aria-label': 'Группы' }, ...gb.groups.map((g) => h('a', {
        class: g.on ? 'fil on' : 'fil',
        href: `#/catalog/${tracker}/${encodeURIComponent(g.first)}`,
        'aria-current': g.on ? 'true' : null,
        'data-key': `grp-${g.id}`,
      }, g.name)));
      bar.before(groups);
    }
    bar.replaceChildren(...gb.sections.map((s) => h('a', {
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
      await restoreDepth(more, () => state, saved.pages);
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
    refresh.stop();
    ctx.listeners.delete(onStatus);
    if (watcher) watcher.disconnect();
    window.removeEventListener('scroll', onScroll);
    clearTimeout(scrollTimer);
  };
}

// entry — раздача в сетке: постер, название, год, качество и формат, раздающие и размер. Раздача, у
// которой ещё нет названия (не догружена), — заглушкой.
export function entry(e, order) {
  const title = e.name || e.title;
  const os = orderStat(e, order);
  return h('a', { class: 'entry', href: `#/release/${e.id}`, 'data-key': `e-${e.id}` },
    poster(e, title),
    e.title
      ? [h('div', { class: 'etitle' }, title), h('div', { class: 'muted small' }, dotted([e.year || null, e.quality || null, formatTag(e.format, e.preferred), altFormatTag(e.preferredAlt)]))]
      : h('div', { class: 'lines', 'aria-label': 'Название ещё не загружено' }, h('div', { class: 'skel', style: { width: '90%' } }), h('div', { class: 'skel', style: { width: '60%' } })),
    h('div', { class: 'stats' },
      h('span', { class: 'stat', title: 'Раздающих' }, icon('arrow_upward', 16), String(e.seeders)),
      os ? h('span', { class: 'stat', title: os.title }, icon(os.icon, 16), os.text) : null,
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
