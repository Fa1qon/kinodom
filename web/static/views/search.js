// Поиск: недавние запросы, состояние каждого трекера, таблица найденного (спека этапа 7, разделы
// 5.4 и 6.3).
import { h, icon, size, poll, keepFocus, offWarn, store, formatTag } from '../ui.js';
import { get, del } from '../api.js';
import { poster } from './catalog.js';

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

export function render(root, r, ctx) {
  const q = (r.query.get('q') || '').trim();
  if (q) store.set('search', '#/search?q=' + encodeURIComponent(q));
  let alive = true;
  const history = h('div', { class: 'history' });
  const trackers = h('div', { class: 'tags' });
  const off = h('div'); // трекеры без адреса (этап 11a): поиск идёт только по остальным
  const table = h('div', { class: 'results' });
  // На узком экране поля поиска в шапке нет — оно здесь.
  const field = h('input', { name: 'q', value: q, placeholder: 'Поиск', 'aria-label': 'Поиск', autocomplete: 'off', enterkeyhint: 'search' });
  const form = h('form', { class: 'search-here', role: 'search', onsubmit: (e) => {
    e.preventDefault();
    const v = field.value.trim();
    if (v) ctx.go('#/search?q=' + encodeURIComponent(v));
  } }, h('label', { class: 'field' }, icon('search'), field));
  root.append(h('div', { class: 'screen' }, form, h('h1', null, q ? `Поиск: «${q}»` : 'Поиск'), history, off, trackers, table));
  const onStatus = (status) => {
    const tr = (status && status.trackers) || {};
    keepFocus(off, () => off.replaceChildren(...OWN.filter((t) => tr[t] && tr[t].state === 'off').map((t) => offWarn(tr[t].text))));
  };
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
    history.replaceChildren(...(items.length ? [
      icon('history', 20, 'Недавние запросы'),
      ...items.map((it) => h('span', { class: it.query.toLowerCase() === q.toLowerCase() ? 'hist on' : 'hist' },
        h('a', { href: '#/search?q=' + encodeURIComponent(it.query), 'data-key': `hist-${it.query}` }, it.query),
        h('button', { type: 'button', 'data-key': `forget-${it.query}`, 'aria-label': `Убрать «${it.query}» из истории`, onclick: () => forget(it.query) }, icon('close', 16)))),
      h('button', { class: 'btn small-btn', type: 'button', 'data-key': 'hist-clear', onclick: () => forget('') }, 'Очистить'),
    ] : []));
  };

  async function forget(query) {
    try {
      await del('/search/history' + (query ? '?q=' + encodeURIComponent(query) : ''));
    } catch {
      // не удалось — список просто останется прежним
    }
    loadHistory();
  }

  loadHistory();
  if (!q) {
    return () => {
      alive = false;
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
    if (first) loadHistory();
    first = false;
    keepFocus(table, () => draw(res));
    if (!keepPolling(res, started, Date.now())) search.stop();
  }, 1000);

  function draw(res) {
    trackers.replaceChildren(...trackerTags(res.trackers, res.results));
    if (!res.results.length) {
      table.replaceChildren(res.complete ? h('p', { class: 'muted' }, 'Ничего не нашлось') : '');
      return;
    }
    table.replaceChildren(
      h('div', { class: 'res-row res-head', 'aria-hidden': 'true' }, h('span', null, ''), h('span', null, 'Раздача'), h('span', null, 'Трекер'), h('span', null, 'Качество'), h('span', null, 'Формат'), h('span', null, 'Размер'), h('span', null, 'Раздают')),
      ...res.results.map((e) => h('a', { class: 'res-row', href: `#/release/${e.id}`, 'data-key': `res-${e.id}` },
        poster(e, e.name || e.title, 'poster thumb'),
        h('span', { class: 'res-title' }, h('span', { class: 'strong ellipsis' }, e.name || e.title), h('span', { class: 'muted small ellipsis' }, e.title)),
        h('span', { class: 'muted' }, trackerLabel(e.tracker)),
        h('span', { class: 'muted' }, e.quality || ''),
        h('span', { class: 'muted' }, formatTag(e.format, e.preferred) || ''),
        h('span', null, size(e.size)),
        h('span', { class: 'seeders' }, icon('arrow_upward', 16), String(e.seeders)))));
  }

  return () => {
    alive = false;
    search.stop();
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
