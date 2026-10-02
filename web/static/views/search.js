// Поиск: недавние запросы, состояние каждого трекера, таблица найденного (спека этапа 7, разделы
// 5.4 и 6.3).
import { h, icon, size, poll, keepFocus, offWarn, store, formatTag } from '../ui.js';
import { get, del } from '../api.js';
import { poster } from './catalog.js';
import { filmHeader } from './kpcat.js';
import { portionEnd } from './channels.js';

// OWN — свои трекеры: страница раздачи, вкладка каталога; остальные — источник поиска Jacred / Jackett и
// трекеры его раздач (11b-Д).
const OWN = ['rutor', 'rutracker'];
const LABELS = {
  rutor: 'Rutor', rutracker: 'Rutracker', jacred: 'Jacred', kinozal: 'Kinozal', nnmclub: 'NNM-Club', lostfilm: 'LostFilm',
  megapeer: 'Megapeer', bitru: 'Bitru', torrentby: 'Torrent.by', ultradox: 'Ultradox', leproduction: 'LE-Production',
  selezen: 'Селезень', rudub: 'RuDub', mazepa: 'Mazepa', toloka: 'Toloka', anilibria: 'AniLibria', anifilm: 'AniFilm',
  baibako: 'BaibaKo', animelayer: 'AnimeLayer',
};
const SEARCH_FOR = 30000; // поиск сервер держит не дольше 30 с

// trackerLabel — название трекера для человека; незнакомый — как пришёл, с заглавной.
export function trackerLabel(name) {
  if (!name) return '';
  return LABELS[name] || name[0].toUpperCase() + name.slice(1);
}

// backTo — «назад» с экрана раздачи: своя — в запомненный раздел каталога её трекера, чужая (из источника
// поиска, каталога у неё нет) — в последний поиск.
export function backTo(rel, savedCatalog, savedSearch) {
  if (!OWN.includes(rel.tracker)) return { href: savedSearch || '#/search', text: 'Поиск' };
  const href = savedCatalog && savedCatalog.startsWith(`#/catalog/${rel.tracker}`) ? savedCatalog : `#/catalog/${rel.tracker}`;
  return { href, text: [trackerLabel(rel.tracker), rel.category].filter(Boolean).join(' · ') };
}

// searchMemory — запомненный поиск для «назад» с раздачи: пришли с фильма «Кинопоиска» — с ним, шапка фильма
// вернётся (ревью 14Г).
export function searchMemory(q, kp) {
  return '#/search?q=' + encodeURIComponent(q) + (kp > 0 ? `&kp=${kp}` : '');
}

// Результаты — порциями (план 17Б): 700 строк перестраивались раз в секунду, пока догружаются постеры, — до 7,7 с
// заморозки на ТВ. RES_FIRST — первая порция, RES_PORTION — следующие.
export const RES_FIRST = 20;
export const RES_PORTION = 40;

// sameIds — тот же набор раздач в том же порядке: опрос меняет только изменившиеся строки.
export function sameIds(a, b) {
  return a.length === b.length && a.every((e, i) => e.id === b[i].id);
}

// resSig — подпись того, что видно в строке результата: строка пересоздаётся, только если она изменилась.
export function resSig(e) {
  return JSON.stringify([e.imageKey, e.name, e.title, e.tracker, e.quality, e.format, e.preferred, e.size, e.seeders]);
}

// searchParts — что на странице поиска (план 16Б): поле всегда; без запроса — история списком и «Очистить
// историю» (если история есть), с запросом — результаты без истории.
export function searchParts(q, items) {
  return { field: true, history: q ? [] : items.map((it) => it.query), clear: !q && items.length > 0, results: !!q };
}

// fieldKey — клавиша key в поле поиска «только для чтения» (ro): OK — открыть ввод (клавиатура ТВ), символ — ввод с
// клавиатуры ПК; остальное — как обычно (стрелки уводят фокус). Поле на ТВ встаёт в фокус без клавиатуры (ревью 16Б:
// фокус на поле после «Поиск» с пульта открывал клавиатуру поверх истории).
export function fieldKey(key, ro) {
  if (!ro) return null;
  if (key === 'Enter') return 'open';
  return key.length === 1 ? 'type' : null;
}

// searchFocus — куда фокус на странице поиска без запроса: вернулись с результатов (prev — страница поиска) — на
// последний запрос истории (он первый: поиск поднял его), иначе — на поле.
export function searchFocus(prev, queries) {
  return prev === '#/search' && queries.length ? `hist-${queries[0]}` : 'search-field';
}

// forgetFocus — после крестика у запроса i: на строку, вставшую на его место, иначе на предыдущую, истории нет — на поле.
export function forgetFocus(queries, i) {
  const rest = queries.filter((_, j) => j !== i);
  if (!rest.length) return 'search-field';
  return `hist-${rest[Math.min(i, rest.length - 1)]}`;
}

export function render(root, r, ctx) {
  const q = (r.query.get('q') || '').trim();
  const kp = Number(r.query.get('kp')) || 0;
  if (q) store.set('search', searchMemory(q, kp));
  let alive = true;
  const history = h('div', { class: 'hist-list' });
  const trackers = h('div', { class: 'tags' });
  const off = h('div'); // трекеры без адреса (этап 11a): поиск идёт только по остальным
  const table = h('div', { class: 'results' });
  const resTail = h('div', { class: 'res-tail' }); // низ построенного: подошёл к экрану — следующая порция (план 17Б)
  // Поле поиска — здесь, на любой ширине (в шапке — только кнопка; план 16Б).
  const field = h('input', { name: 'q', value: q, placeholder: 'Поиск', 'aria-label': 'Поиск', autocomplete: 'off', enterkeyhint: 'search', 'data-key': 'search-field' });
  const form = h('form', { class: 'search-here', role: 'search', onsubmit: (e) => {
    e.preventDefault();
    const v = field.value.trim();
    if (v) ctx.go('#/search?q=' + encodeURIComponent(v));
  } }, h('label', { class: 'field' }, icon('search'), field));
  // Поле «только для чтения», пока его не открыли: фокус на нём не вызывает клавиатуру ТВ; OK, касание или символ —
  // ввод. Ушли с поля — снова только для чтения (стрелками на него — без клавиатуры). OK — только снять «только для
  // чтения»: клавиатуру показывает сам WebView по OK; preventDefault или перефокус её не открывали (эмулятор ТВ).
  const openField = () => {
    field.readOnly = false;
  };
  field.readOnly = true;
  field.addEventListener('keydown', (e) => {
    if (fieldKey(e.key, field.readOnly)) openField();
  });
  field.addEventListener('pointerdown', openField);
  field.addEventListener('blur', () => {
    if (document.activeElement !== field) field.readOnly = true;
  });
  // Пришли с карточки «Кинопоиска» (план 14Г) — над результатами шапка фильма.
  const film = h('div');
  if (kp > 0) {
    get(`/kpcat/films/${kp}`).then((f) => {
      if (alive) film.replaceChildren(filmHeader(f));
    }, () => {});
  }
  root.append(h('div', { class: 'screen' }, form, h('h1', null, q ? `Поиск: «${q}»` : 'Поиск'), film, history, off, trackers, table, resTail));
  const onStatus = (status) => {
    const tr = (status && status.trackers) || {};
    keepFocus(off, () => off.replaceChildren(...OWN.filter((t) => tr[t] && tr[t].state === 'off').map((t) => offWarn(tr[t].text))));
  };
  let shownQueries = []; // запросы истории на экране — для фокуса после крестика
  ctx.listeners.add(onStatus);
  onStatus(ctx.status);

  const loadHistory = async () => {
    let items = [];
    try {
      items = await get('/search/history');
    } catch {
      return;
    }
    if (!alive) return;
    // История — списком сверху вниз (на ТВ — «вниз-вниз»), только на странице без запроса (план 16Б).
    const parts = searchParts(q, items);
    shownQueries = parts.history;
    keepFocus(history, () => history.replaceChildren(
      ...parts.history.map((query) => h('div', { class: 'hist-row' },
        h('a', { class: 'hist-q', href: '#/search?q=' + encodeURIComponent(query), 'data-key': `hist-${query}` }, icon('history', 18), h('span', null, query)),
        h('button', { class: 'sq', type: 'button', 'data-key': `forget-${query}`, 'aria-label': `Убрать «${query}» из истории`, onclick: () => forget(query) }, icon('close', 18)))),
      parts.clear ? h('button', { class: 'btn', type: 'button', 'data-key': 'hist-clear', onclick: () => forget('') }, 'Очистить историю') : '',
    ));
  };

  async function forget(query) {
    // Фокус после крестика — на соседнюю строку, после «Очистить» — на поле (ревью 16Б: уходил в никуда).
    const next = query ? forgetFocus(shownQueries, shownQueries.indexOf(query)) : 'search-field';
    try {
      await del('/search/history' + (query ? '?q=' + encodeURIComponent(query) : ''));
    } catch {
      // не удалось — список просто останется прежним
    }
    await loadHistory();
    const el = root.querySelector(`[data-key="${CSS.escape(next)}"]`);
    if (el) el.focus({ preventScroll: true });
  }

  if (!q) {
    // Пришли кнопкой «Поиск» — фокус на поле (OK — клавиатура, «вниз» — история); вернулись с результатов — на
    // последний запрос истории.
    loadHistory().then(() => {
      const a = document.activeElement;
      if (!alive || (a && a !== document.body && root.contains(a))) return;
      const el = root.querySelector(`[data-key="${CSS.escape(searchFocus(ctx.prev, shownQueries))}"]`);
      if (el) el.focus({ preventScroll: true });
    });
    return () => {
      alive = false;
      ctx.listeners.delete(onStatus);
    };
  }

  // Первый запрос пишет историю, повторы опроса — с poll=1 (спека этапа 7, раздел 5.4). Опрос идёт и
  // после конца поиска, пока у найденного догружаются страницы с постерами (keepPolling).
  const started = Date.now();
  let first = true;
  const search = poll(async () => {
    let res;
    try {
      res = await get('/search?q=' + encodeURIComponent(q) + (first ? '' : '&poll=1'));
    } catch (e) {
      search.stop();
      table.replaceChildren(h('p', { class: 'error' }, e.message));
      return;
    }
    if (!alive) return;
    first = false;
    keepFocus(table, () => draw(res));
    if (!keepPolling(res, started, Date.now())) search.stop();
  }, 1000);

  // Лента результатов (план 17Б): построено built строк; refs — id → строка и её подпись (resSig).
  let rows = [];
  let built = 0;
  let drawn = null; // набор, по которому построена таблица: тот же — опрос меняет строки на месте
  const refs = new Map();

  function draw(res) {
    trackers.replaceChildren(...trackerTags(res.trackers, res.results));
    if (!res.results.length) {
      table.replaceChildren(res.complete ? h('p', { class: 'muted' }, 'Ничего не нашлось') : '');
      rows = [];
      built = 0;
      drawn = null;
      refs.clear();
      return;
    }
    rows = res.results;
    if (drawn && sameIds(rows, drawn)) {
      for (const e of rows.slice(0, built)) {
        const r = refs.get(e.id);
        const sig = resSig(e);
        if (!r || r.sig === sig) continue;
        const el = resRow(e);
        r.el.replaceWith(el);
        refs.set(e.id, { el, sig });
      }
      drawn = rows;
      return;
    }
    // Набор или порядок сменился (поиск ещё идёт) — заново, не меньше построенного: фокус по data-key остаётся.
    const want = Math.max(RES_FIRST, built);
    built = 0;
    refs.clear();
    table.replaceChildren(
      h('div', { class: 'res-row res-head', 'aria-hidden': 'true' }, h('span', null, ''), h('span', null, 'Раздача'), h('span', null, 'Трекер'), h('span', null, 'Качество'), h('span', null, 'Формат'), h('span', null, 'Размер'), h('span', null, 'Раздают')),
      ...buildRows(portionEnd(rows.length, 0, want)));
    drawn = rows;
    rewatch();
  }

  // buildRows — строки ленты с built до to.
  function buildRows(to) {
    const out = rows.slice(built, to).map((e) => {
      const el = resRow(e);
      refs.set(e.id, { el, sig: resSig(e) });
      return el;
    });
    built = to;
    return out;
  }

  function resRow(e) {
    return h('a', { class: 'res-row', href: `#/release/${e.id}`, 'data-key': `res-${e.id}` },
      poster(e, e.name || e.title, 'poster thumb'),
      h('span', { class: 'res-title' }, h('span', { class: 'strong ellipsis' }, e.name || e.title), h('span', { class: 'muted small ellipsis' }, e.title)),
      h('span', { class: 'muted' }, trackerLabel(e.tracker)),
      h('span', { class: 'muted' }, e.quality || ''),
      h('span', { class: 'muted' }, formatTag(e.format, e.preferred) || ''),
      h('span', null, size(e.size)),
      h('span', { class: 'seeders' }, icon('arrow_upward', 16), String(e.seeders)));
  }

  // Следующая порция — низ построенного подошёл к экрану или фокус в одной из трёх последних строк.
  const moreRows = () => {
    if (!alive || built >= rows.length) return;
    table.append(...buildRows(portionEnd(rows.length, built, RES_PORTION)));
    rewatch();
  };
  const watcher = typeof IntersectionObserver === 'function'
    ? new IntersectionObserver((es) => {
      if (es.some((x) => x.isIntersecting)) moreRows();
    }, { rootMargin: '600px 0px' })
    : null;
  // rewatch — наблюдатель сообщает только смену «в зоне / вне зоны»: после порции — переподписка (как в «Каналах»).
  function rewatch() {
    if (watcher) {
      watcher.unobserve(resTail);
      watcher.observe(resTail);
    } else if (resTail.getBoundingClientRect().top < window.innerHeight + 600) {
      moreRows();
    }
  }
  table.addEventListener('focusin', (e) => {
    const row = e.target.closest && e.target.closest('a.res-row');
    if (row && [...table.querySelectorAll('a.res-row')].slice(-3).includes(row)) moreRows();
  });

  return () => {
    alive = false;
    search.stop();
    if (watcher) watcher.disconnect();
    ctx.listeners.delete(onStatus);
  };
}

// POSTERS_FOR — после конца поиска постеры найденного ждём не дольше 2 минут от начала (их страницы
// догружаются по одной в секунду на трекер).
const POSTERS_FOR = 120000;

// keepPolling — опрашивать ли поиск дальше: трекеры ещё ищут (до 30 с) или у найденного догружаются
// страницы с постерами (до 2 минут от начала) — замечание № 10 этапа 11b.
export function keepPolling(res, startedAt, now) {
  if (!res.complete) return now - startedAt <= SEARCH_FOR;
  return now - startedAt <= POSTERS_FOR && res.results.some((e) => e.detailsPending);
}

// tagStates — состояние каждого трекера в поиске: сколько найдено, ищет, текст ошибки. Свои — первыми, источник
// поиска — после; его число — раздачи чужих трекеров (темы Rutor и Rutracker из него — в числе своих).
export function tagStates(trackers, results) {
  const count = {};
  for (const e of results) {
    const k = OWN.includes(e.tracker) ? e.tracker : '';
    count[k] = (count[k] || 0) + 1;
  }
  const names = [...OWN.filter((t) => t in trackers), ...Object.keys(trackers).filter((t) => !OWN.includes(t)).sort()];
  return names.map((t) => {
    const s = trackers[t];
    const label = trackerLabel(t);
    if (s === 'ok') return { state: 'ok', text: `${label} · ${count[OWN.includes(t) ? t : ''] || 0}` };
    if (s === 'идёт') return { state: 'busy', text: `${label} · ищет…` };
    return { state: 'warn', text: s.startsWith(label) ? s : `${label} · ${s}` };
  });
}

// trackerTags — теги tagStates. Ими же пользуется «Искать на трекерах» на экране раздачи.
export function trackerTags(trackers, results) {
  return tagStates(trackers, results).map(({ state, text }) => {
    if (state === 'ok') return h('span', { class: 'tag ok-tag' }, icon('check', 18, 'Готово'), text);
    if (state === 'busy') return h('span', { class: 'tag busy-tag' }, icon('progress_activity', 18), text);
    return h('span', { class: 'tag warn-tag' }, icon('warning', 18), text);
  });
}
