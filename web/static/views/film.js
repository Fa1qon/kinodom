// Страница фильма (#/film/<номер Кинопоиска>) — в духе «полной карточки» Lampa (просьба 2026-11-08): во главе фильм,
// а не раздача. Сверху постер на размытом фоне, название, оценки, «Смотреть» (если фильм уже в медиатеке — любой
// раздачей), «Хочу посмотреть»; сразу под ними — раздачи этого фильма со всех трекеров, с сортировкой и отбором по
// качеству и сезону. Раздача ведёт на свою страницу (файлы, серии, «Скачать»).
import { h, icon, size, poll, keepFocus, rating, plural, formatTag } from '../ui.js';
import { get, post } from '../api.js';
import { filmMatch, filmQuery, isSeries, latinOriginal } from './kpcat.js';
import { trackerLabel, keepPolling, trackerTags } from './search.js';
import { ownedMap, forgetOwned } from '../owned.js';

const SORTS = [['seeders', 'Раздающие'], ['size', 'Размер'], ['added', 'Новые']];

// qualityOf — ступень качества раздачи для отбора: «2160p», «1080p», «720p» или «Другое».
export function qualityOf(e) {
  const s = `${e.quality || ''} ${e.title || ''}`;
  if (/2160p|4k|uhd/i.test(s)) return '2160p';
  if (/1080[pi]/i.test(s)) return '1080p';
  if (/720p/i.test(s)) return '720p';
  return 'Другое';
}

// seasonOf — сезон раздачи сериала по заголовку («S01» → 1, «Сезон: 3» → 3); 0 — не указан.
export function seasonOf(e) {
  const m = /S(\d{1,2})/i.exec(e.season || '') || /Сезон[:\s]*(\d{1,2})/i.exec(e.title || '');
  return m ? Number(m[1]) : 0;
}

// sortRows — раздачи по выбранному порядку; при равенстве — больше раздающих выше.
export function sortRows(rows, by) {
  const key = {
    seeders: (e) => e.seeders,
    size: (e) => e.size,
    added: (e) => (e.added ? Date.parse(e.added) : 0),
  }[by] || ((e) => e.seeders);
  return [...rows].sort((a, b) => key(b) - key(a) || b.seeders - a.seeders);
}

// backdrop — размытый фон из постера: постер рисуется на холст 24×36 и растягивается — размытие даёт само
// растяжение (как у Lampa), без CSS-фильтра, который тормозит на слабом телевизоре.
function backdrop(src) {
  const c = h('canvas', { class: 'full-bg', width: 24, height: 36, 'aria-hidden': 'true' });
  if (!src) return c;
  const img = new Image();
  img.onload = () => {
    try {
      c.getContext('2d').drawImage(img, 0, 0, 24, 36);
      c.classList.add('on');
    } catch {
      // постер с чужого адреса — без фона
    }
  };
  img.src = src;
  return c;
}

export function render(root, r) {
  const kp = Number(r.parts[1]) || 0;
  let alive = true;
  let film = null;
  let owned = null; // карточка медиатеки этого фильма; null — нет
  let res = null; // последний ответ поиска
  let sort = 'seeders';
  let quality = '';
  let season = 0;
  let showOther = false;
  let wantDone = false;
  let have = new Map(); // номер раздачи → «done» (скачана) или «loading» (качается): отметка в списке
  let searchStop = () => {};

  const head = h('div', { class: 'full-head' });
  const filters = h('div', { class: 'full-filters', 'data-row': '' });
  const list = h('div', { class: 'torrents' });
  const state = h('div', { class: 'tags full-trackers' });
  root.append(h('div', { class: 'screen full' }, head, h('h2', { class: 'full-h2' }, 'Раздачи'), state, filters, list));

  async function load() {
    try {
      const f = await get(`/kpcat/films/${kp}`);
      film = { ...f };
    } catch {
      film = null;
    }
    // Описание и вид (фильм/сериал) — из пакета рекомендаций: в каталоге Кинопоиска описания нет.
    try {
      const v = await get(`/recs/films?ids=${kp}`);
      const x = v.items && v.items[0];
      if (x) {
        film = film || { id: kp, title: x.title, original: x.titleOrig, year: x.year, poster: x.poster, genres: x.genres ? x.genres.split(', ') : [] };
        film.description = x.description || '';
        film.kind = film.kind || x.kind;
        if (!film.kinopoisk && x.kpRating) film.kinopoisk = x.kpRating;
      }
    } catch {
      // без описания
    }
    if (!alive) return;
    if (!film) {
      head.replaceChildren(h('p', { class: 'error' }, 'Фильм не найден'));
      return;
    }
    owned = (await ownedMap()).get(kp) || null;
    // Какие раздачи уже скачаны (у сериала их бывает несколько — по сезонам и разным трекерам).
    try {
      const d = await get('/downloads');
      for (const x of d.items || []) {
        if (!x.release || !x.release.id) continue;
        const st = x.percent >= 100 ? 'done' : 'loading';
        if (have.get(x.release.id) !== 'done') have.set(x.release.id, st);
      }
    } catch {
      // без отметок
    }
    if (!alive) return;
    drawHead();
    startSearch();
  }

  function drawHead() {
    const f = film;
    const meta = [f.year || null, isSeries(f) ? 'сериал' : null, (f.countries || []).slice(0, 2).join(', ') || null].filter(Boolean).join(' · ');
    const rates = [
      f.kinopoisk > 0 ? h('div', { class: 'rate' }, h('b', null, rating(f.kinopoisk)), h('span', null, 'КП')) : null,
      f.imdb > 0 ? h('div', { class: 'rate' }, h('b', null, rating(Math.round(f.imdb * 10) / 10)), h('span', null, 'IMDb')) : null,
    ];
    const buttons = [
      owned ? h('a', { class: 'btn big inv', href: `#/library/${encodeURIComponent(owned.key)}`, 'data-key': 'film-watch', 'data-nav-main': '' },
        icon('play_arrow', 22), owned.downloading ? 'Смотреть · качается' : 'Смотреть') : null,
      h('button', { class: 'btn big', type: 'button', 'data-key': 'film-torrents', onclick: () => {
        const first = list.querySelector('a');
        if (first) first.focus();
      } }, icon('download', 22), 'Раздачи'),
      h('button', { class: 'btn big', type: 'button', 'data-key': 'film-want', disabled: wantDone, onclick: async () => {
        try {
          await post('/recs/mark', { kpId: kp, mark: 'want' });
          wantDone = true;
          keepFocus(head, drawHead);
        } catch {
          // рекомендации не установлены — кнопка просто не сработает
        }
      } }, icon('bookmark', 22), wantDone ? 'В «Хочу посмотреть»' : 'Хочу посмотреть'),
    ];
    const poster = h('div', { class: 'full-poster' }, f.poster ? h('img', { src: f.poster, alt: '' }) : null);
    keepFocus(head, () => head.replaceChildren(
      backdrop(f.poster),
      h('div', { class: 'full-body' },
        poster,
        h('div', { class: 'full-right' },
          h('div', { class: 'full-meta' }, meta),
          h('h1', { class: 'full-title' }, f.title),
          latinOriginal(f) ? h('div', { class: 'full-orig' }, latinOriginal(f)) : null,
          h('div', { class: 'full-rates' }, ...rates, (f.genres || []).length ? h('div', { class: 'full-genres' }, f.genres.slice(0, 4).join(', ')) : null),
          owned ? h('div', { class: 'full-have' }, icon('download_done', 18), owned.downloading ? 'Качается — уже можно смотреть' : 'Уже есть в медиатеке') : null,
          h('div', { class: 'full-buttons' }, ...buttons),
          f.description ? h('p', { class: 'full-desc' }, f.description) : null))));
  }

  function startSearch() {
    const { q, name } = filmQuery(film);
    let query = q;
    let first = true;
    const started = Date.now();
    const p = poll(async () => {
      let v;
      try {
        v = await get('/search?poll=1&q=' + encodeURIComponent(query));
      } catch (e) {
        p.stop();
        list.replaceChildren(h('p', { class: 'error' }, e.message));
        return;
      }
      if (!alive) return;
      // Точный запрос (с оригиналом) пуст — тот же поиск по одному названию.
      if (first && v.complete && !v.results.length && name && query !== name) {
        query = name;
        return;
      }
      first = false;
      res = v;
      draw();
      if (!keepPolling(v, started, Date.now())) p.stop();
    }, 1000);
    searchStop = () => p.stop();
  }

  function draw() {
    if (!res) return;
    state.replaceChildren(...trackerTags(res.trackers, res.results));
    const hit = res.results.filter((e) => filmMatch(e, film));
    const other = res.results.filter((e) => !filmMatch(e, film));
    const base = hit.length ? (showOther ? hit.concat(other) : hit) : other;
    const series = isSeries(film);
    const qualities = ['2160p', '1080p', '720p', 'Другое'].filter((x) => base.some((e) => qualityOf(e) === x));
    const seasons = series ? [...new Set(base.map(seasonOf).filter(Boolean))].sort((a, b) => a - b) : [];
    if (quality && !qualities.includes(quality)) quality = '';
    if (season && !seasons.includes(season)) season = 0;
    // Скачанные раздачи — первыми: фильм уже есть, с них и смотреть.
    const rows = sortRows(base.filter((e) => (!quality || qualityOf(e) === quality) && (!season || seasonOf(e) === season)), sort)
      .sort((a, b) => (have.has(b.id) ? 1 : 0) - (have.has(a.id) ? 1 : 0));
    const chip = (on, text, key, fn) => h('button', { class: on ? 'fil on' : 'fil', type: 'button', 'data-key': key, onclick: () => { fn(); keepFocus(root, draw); } }, text);
    keepFocus(filters, () => filters.replaceChildren(
      ...SORTS.map(([id, text]) => chip(sort === id, text, `sort-${id}`, () => { sort = id; })),
      qualities.length > 1 ? h('span', { class: 'full-sep' }) : null,
      ...(qualities.length > 1 ? [chip(!quality, 'Любое', 'q-all', () => { quality = ''; }), ...qualities.map((x) => chip(quality === x, x, `q-${x}`, () => { quality = x; }))] : []),
      seasons.length > 1 ? h('span', { class: 'full-sep' }) : null,
      ...(seasons.length > 1 ? [chip(!season, 'Все сезоны', 's-all', () => { season = 0; }), ...seasons.map((x) => chip(season === x, `Сезон ${x}`, `s-${x}`, () => { season = x; }))] : []),
    ));
    const tail = [];
    if (!hit.length && other.length) tail.push(h('p', { class: 'muted' }, 'Раздач именно этого фильма не нашлось — ниже раздачи с похожим названием.'));
    if (hit.length && other.length && !showOther) {
      tail.push(h('button', { class: 'btn', type: 'button', 'data-key': 'res-other', onclick: () => { showOther = true; keepFocus(root, draw); } },
        `Ещё ${other.length} ${plural(other.length, 'раздача', 'раздачи', 'раздач')} с похожим названием`));
    }
    const empty = !rows.length ? h('p', { class: 'muted' }, res.complete ? 'Раздач не нашлось' : 'Ищу на трекерах…') : null;
    keepFocus(list, () => list.replaceChildren(empty || '', ...rows.slice(0, 120).map(item), ...tail));
  }

  // item — раздача (как torrent-item у Lampa): заголовок целиком, ниже трекер, дата, качество и формат, справа —
  // раздающие, качающие и размер.
  function item(e) {
    const st = have.get(e.id);
    return h('a', { class: st ? 'torrent-item have-it' : 'torrent-item', href: `#/release/${e.id}`, 'data-key': `t-${e.id}` },
      h('div', { class: 'ti-title' }, e.title || e.name),
      h('div', { class: 'ti-details' },
        st ? h('span', { class: 'ti-have' }, icon(st === 'done' ? 'download_done' : 'download', 15), st === 'done' ? 'Скачано' : 'Качается') : null,
        h('span', { class: 'ti-tracker' }, trackerLabel(e.tracker)),
        e.added ? h('span', null, new Date(e.added).toLocaleDateString('ru-RU')) : null,
        e.quality ? h('span', { class: 'ti-chip' }, e.quality) : null,
        e.format ? h('span', { class: 'ti-chip' }, formatTag(e.format, e.preferred)) : null,
        h('span', { class: 'grow' }),
        h('span', { class: 'ti-num', title: 'Раздают' }, icon('arrow_upward', 15), String(e.seeders)),
        h('span', { class: 'ti-num dim', title: 'Качают' }, icon('arrow_downward', 15), String(e.leechers)),
        h('span', { class: 'ti-size' }, size(e.size))));
  }

  forgetOwned();
  load();
  return () => {
    alive = false;
    searchStop();
  };
}
