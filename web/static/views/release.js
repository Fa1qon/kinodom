// Раздача: единая кнопка «Смотреть» подготавливает файл и запускает плеер; ниже — серии,
// «Другие раздачи» и поиск на трекерах.
import { h, icon, size, speed, rating, minutes, ready, poll, copyText, store, plural, shortNames, keepFocus, fileFormat, formatTag, openPlayer, confirmDialog, fill } from '../ui.js';
import { get, post, put, del } from '../api.js';
import { whereStopped, resumeIndex } from './history.js';
import { poster, returnTo } from './catalog.js';
import { trackerTags, trackerLabel, backTo } from './search.js';
import { watchTarget, webBlockedFormat, playHash, externalLink } from './player-parts.js';
import { appBridge } from './tvkit.js';
import { isClient } from '../client-ui.js';

const PENDING_FOR = 120000; // догрузку страницы раздачи ждём не дольше 2 минут (трекер мог лечь)
const POSTER_FOR = 60000; // постер после страницы — не дольше минуты: сервер пробует его раз в минуту (спека 11b, 14.2)

// releaseDone — опрос раздачи можно остановить: страницу ждали дольше PENDING_FOR, или страница пришла (pageAt)
// и постер есть либо ждали его POSTER_FOR (финальное ревью 11b-Ж: раньше опрос вставал на странице, и
// постер, начатый при открытии, на экране не появлялся).
export function releaseDone(rel, started, pageAt, now) {
  if (rel.detailsPending) return now - started > PENDING_FOR;
  return !!rel.imageKey || now - pageAt > POSTER_FOR;
}
const SEARCH_FOR = 30000; // поиск других раздач сервер держит не дольше 30 с
const FORMATS_FOR = 120000; // формат найденных ждём не дольше 2 минут: их страницы догружаются
const MISSING_EVERY = 3000; // раздача не открыта (404) — её могут открыть с другого устройства: проверяем раз в 3 с
const OTHERS_EVERY = 10000; // «Другие раздачи» перечитываются, пока экран открыт
const COPIED_FOR = 2000; // «Скопировано» видно 2 с, перерисовка панели его не сбивает (Х25)

// seasonLabel — какой сезон и какие серии в раздаче — крупно у «Скачать» (заказчик 2026-10-01): строка сезона
// из заголовка («S01», «S02E01-08», «02x13 из 13», «Сезон: 1-3, Серии: 1-24 из 24», «1-5 серий из 5», у t1 —
// «01-07 из 08») — «Сезон 2 · серии 1–8», «Сезоны 1–3 · серии 1–24 из 24»; «11 серий из 11» — сколько серий,
// а не номер последней (ревью 14А): «серии 1–11 из 11»; не сериал — "".
export function seasonLabel(season) {
  const s = (season || '').trim();
  if (!s) return '';
  const range = (a, b) => (b && b !== a ? `${Number(a)}–${Number(b)}` : String(Number(a)));
  let seasons = null; // [from, to]
  let eps = null; // [from, to, of]
  let m;
  if ((m = s.match(/S(\d{1,2})(?:-S?(\d{1,3}))?(?:E(\d{1,3})(?:-E?(\d{1,3}))?)?/i))) {
    if (m[3]) {
      seasons = [m[1]];
      eps = [m[3], m[4]];
    } else {
      seasons = [m[1], m[2]];
    }
  } else if ((m = s.match(/(\d{1,2})x(\d{1,3})(?:-(\d{1,3}))?(?:\s+из\s+(\d+))?/i))) {
    seasons = [m[1]];
    eps = [m[2], m[3], m[4]];
  }
  if (!seasons && (m = s.match(/(?:сезон|season)\s*:?\s*(\d+)(?:\s*-\s*(\d+))?/i) || s.match(/(\d+)(?:\s*-\s*(\d+))?\s+(?:сезон|season)/i))) {
    seasons = [m[1], m[2]];
  }
  if (!eps && (m = s.match(/(?:серии|серия)\s*:?\s*(\d+)(?:\s*-\s*(\d+))?(?:\s+из\s+(\d+))?/i))) {
    eps = [m[1], m[2], m[3]];
  } else if (!eps && (m = s.match(/(\d+)(?:\s*-\s*(\d+))?\s+сери[йияю]\s+из\s+(\d+)/i))) {
    eps = m[2] ? [m[1], m[2], m[3]] : ['1', m[1], m[3]]; // «8 серий из 10» — первые восемь
  } else if (!eps && !seasons && (m = s.match(/^\[?(\d+)(?:\s*-\s*(\d+))?\s+из\s+(\d+)\]?$/i))) {
    eps = [m[1], m[2], m[3]];
  }
  const parts = [];
  if (seasons) parts.push((seasons[1] && seasons[1] !== seasons[0] ? 'Сезоны ' : 'Сезон ') + range(seasons[0], seasons[1]));
  if (eps) {
    const many = eps[1] && eps[1] !== eps[0];
    let t = (many ? 'серии ' : 'серия ') + range(eps[0], eps[1]);
    if (eps[2]) t += ` из ${Number(eps[2])}`;
    parts.push(parts.length ? t : t[0].toUpperCase() + t.slice(1));
  }
  return parts.join(' · ');
}

// variantSeasonGroups — группирует соседние раздачи по сезону, чтобы S01 и S02 не выглядели
// плоским списком вариантов качества. Раздача без распознанного сезона остаётся в «Без сезона».
export function variantSeasonGroups(items) {
  const groups = new Map();
  const keyOf = (e) => {
    const s = (e.season || '').trim();
    let m = /S(\d{1,2})(?:-S?(\d{1,2}))?/i.exec(s);
    if (!m) m = /(?:сезон|season)\s*:?\s*(\d+)(?:\s*-\s*(\d+))?/i.exec(s);
    if (!m) m = /(\d{1,2})x\d+/i.exec(s);
    return m ? `s${Number(m[1])}${m[2] ? `-${Number(m[2])}` : ''}` : 'none';
  };
  for (const e of items || []) {
    const key = keyOf(e);
    if (!groups.has(key)) groups.set(key, { key, title: key === 'none' ? 'Без сезона' : (seasonLabel(e.season) || 'Сезон'), items: [] });
    groups.get(key).items.push(e);
  }
  return [...groups.values()].sort((a, b) => (a.key === 'none' ? 9999 : Number(a.key.slice(1))) - (b.key === 'none' ? 9999 : Number(b.key.slice(1))));
}

// fileSeasonGroups — сезон из пути/имени файла, когда заголовок раздачи не содержит S01.
// Это покрывает обычную структуру «Название/S01/S01E01.mkv».
export function fileSeasonGroups(files) {
  const groups = new Map();
  const seasonOf = (f) => {
    const s = String(f && f.name || '');
    let m = /(?:^|[\\/._ -])S(\d{1,2})(?:E\d{1,3})?(?=$|[\\/._ -])/i.exec(s);
    if (!m) m = /(?:^|[\\/._ -])(?:season|сезон)[ ._-]*(\d{1,2})(?=$|[\\/._ -])/i.exec(s);
    return m ? Number(m[1]) : 0;
  };
  for (const f of files || []) {
    const season = seasonOf(f);
    const key = season > 0 ? `s${season}` : 'none';
    if (!groups.has(key)) groups.set(key, { key, season, title: season > 0 ? `Сезон ${season}` : 'Без сезона', items: [] });
    groups.get(key).items.push(f);
  }
  return [...groups.values()].sort((a, b) => (a.season || 9999) - (b.season || 9999));
}

export function fileSeasonLabel(files) {
  const seasons = [...new Set(fileSeasonGroups(files).filter((g) => g.season > 0).map((g) => g.season))];
  if (!seasons.length) return '';
  if (seasons.length === 1) return `Сезон ${seasons[0]}`;
  const runs = [];
  let start = seasons[0];
  let prev = start;
  for (const n of seasons.slice(1)) {
    if (n !== prev + 1) { runs.push(start === prev ? String(start) : `${start}–${prev}`); start = n; }
    prev = n;
  }
  runs.push(start === prev ? String(start) : `${start}–${prev}`);
  return `Сезоны ${runs.join(', ')}`;
}

// episodeAction — OK на строке серии (замечание № 3 этапа 11b): до «Скачать» — окно «Скачать?»,
// после — выбрать файл панели; пока идёт действие — ничего.
export function episodeAction(downloadingNow, busyNow) {
  if (busyNow) return 'none';
  return downloadingNow ? 'pick' : 'confirm';
}

// bestFile — единый выбор серии для главной кнопки: явный выбор и продолжение важнее
// автоматического выбора, но готовый файл всегда выигрывает у файла, который только стоит
// в очереди. Так «Смотреть» не отправляет человека ждать первый файл раздачи, если другая
// серия уже доступна.
export function bestFile(files, history = [], chosen = null, focus = -1) {
  const fs = files || [];
  if (!fs.length) return null;
  const byIndex = (i) => fs.find((f) => f.index === i);
  const explicit = byIndex(chosen);
  if (explicit) return explicit;
  const resume = resumeIndex(fs.map((f) => f.index), history);
  const resumed = byIndex(resume);
  if (resumed && resumed.readiness && resumed.readiness !== 'none') return resumed;
  const rank = { done: 4, smooth: 3, wait: 2, none: 0 };
  const ready = fs.filter((f) => (rank[f.readiness || 'none'] || 0) > 0)
    .sort((a, b) => (rank[b.readiness || 'none'] || 0) - (rank[a.readiness || 'none'] || 0) || a.index - b.index);
  if (ready.length) return ready[0];
  const focused = byIndex(focus);
  return focused || resumed || fs[0];
}

export function render(root, r, ctx) {
  const compact = isClient();
  const id = r.parts[1];
  let alive = true;
  let rel = null; // /releases/{id}
  let st = null; // /torrents/{hash}; null — раздача не открыта
  let chosen = null; // файл панели, выбранный человеком; null — файл в фокусе
  let actionError = ''; // ошибка «Скачать» или «Смотреть»
  let busy = false;
  let followBusy = false; // «Следить» отправляется
  let torrentPoll = null;
  let focusNext = ''; // после «Скачать» с пульта ТВ — куда перевести фокус, когда появится «Смотреть»
  const started = Date.now();
  let pageAt = 0; // когда пришла страница раздачи
  let variants = null; // /releases/{id}/variants — «Другие раздачи»
  let searchPoll = null; // «Искать на трекерах» идёт
  let searchStarted = 0;
  let fetchedAt = 0;
  let searchError = '';
  let progress = []; // /history/{hash} — где остановились на этом устройстве (спека этапа 8, раздел 7.5)
  let historyPoll = null;
  let headKey = ''; // данные заголовка при последней отрисовке: пока догрузка, постер не пересоздаётся каждую секунду
  let missingAt = 0; // когда /torrents/{hash} последний раз ответил 404
  let copied = null; // состояние копирования ссылки
  let pendingWatch = null; // файл, который нужно запустить после минимального буфера
  let pendingExternal = null; // файл, который нужно открыть во внешнем плеере после подготовки

  const back = h('div');
  const cover = h('div', { class: 'rel-cover' });
  const info = h('div', { class: 'rel-info' });
  const live = h('div', { class: 'rel-live' });
  const side = h('aside', { class: 'panel', 'aria-label': 'Просмотр', 'data-nav-column': true });
  const others = h('section', { class: 'others', 'aria-label': 'Другие раздачи' });
  const preload = h('div', { class: 'release-preload', hidden: true, 'aria-live': 'polite' }, icon('progress_activity'), h('span', null, 'Подготавливаем просмотр…'));
  const title = h('h1', { class: 'client-release-title' });
  const extra = h('div', { class: 'client-release-extra' });
  const main = h('div', { class: 'rel-main', 'data-nav-column': true }, info, live, others);
  const grid = h('div', { class: 'rel-grid' }, cover, main, side);
  const tabs = null;
  function showTab() {
    if (!compact) return;
    // На ТВ и телефоне описание и действия находятся на одном экране с сериями.
    // Переключатель «Просмотр / Подробнее» скрывал полезное описание за лишним OK.
    cover.hidden = info.hidden = live.hidden = side.hidden = others.hidden = false;
    extra.hidden = true;
  }
  showTab();
  root.append(h('div', { class: compact ? 'screen release client-release' : 'screen release' }, back,
    compact ? title : null, tabs, grid));
  root.append(preload);
  // «Другие раздачи» — при открытии и дальше раз в 10 с (раздачу того же фильма могли найти или открыть).
  const othersPoll = poll(async () => {
    if (searchPoll) return;
    let v;
    try {
      v = await get(`/releases/${id}/variants` + (variants && variants.search ? '?search=1&poll=1' : ''));
    } catch {
      return;
    }
    if (alive && !searchPoll) {
      variants = v;
      drawOthers();
    }
  }, OTHERS_EVERY);

  const releasePoll = poll(async () => {
    try {
      rel = await get(`/releases/${id}`);
    } catch (e) {
      releasePoll.stop();
      info.replaceChildren(h('p', { class: 'error' }, e.status === 404 ? 'Такой раздачи нет' : e.message));
      return;
    }
    if (!alive) return;
    const key = JSON.stringify([rel.name, rel.title, rel.imageKey, rel.description, rel.original, rel.year, rel.kinopoisk,
      rel.seeders, rel.quality, rel.format, rel.preferred, rel.size, rel.trackerUrl, rel.detailsPending, rel.category]);
    if (key !== headKey) {
      headKey = key;
      drawHead();
    }
    drawLive();
    // Найденное поиском догружается при открытии — опрашиваем, пока страница не загрузится, а после неё —
    // пока не придёт постер.
    if (!rel.detailsPending && !pageAt) pageAt = Date.now();
    if (releaseDone(rel, started, pageAt, Date.now())) releasePoll.stop();
    if (rel.hash && !torrentPoll) watchTorrent();
    if (rel.hash && !historyPoll) watchHistory();
  }, 1000);

  // watchHistory — места и отметки этого устройства раз в 10 с: VLC сообщает место по ходу просмотра.
  function watchHistory() {
    historyPoll = poll(async () => {
      try {
        progress = (await get(`/history/${rel.hash}`)).files;
      } catch {
        return;
      }
      if (alive) drawLive();
    }, 10000);
  }

  const progressOf = (f) => progress.find((p) => p.index === f.index) || null;

  // watchTorrent — состояние раздачи раз в секунду, пока она качается (спека этапа 7, раздел 4). Раздача
  // не открыта (404) — проверка раз в 3 с: «Скачать» могли нажать на другом устройстве (Х26).
  function watchTorrent() {
    torrentPoll = poll(async () => {
      if (!st && missingAt && Date.now() - missingAt < MISSING_EVERY) return;
      try {
        st = await get(`/torrents/${rel.hash}`);
        missingAt = 0;
      } catch (e) {
        st = null;
        if (e.status === 404) missingAt = Date.now();
        else actionError = e.message;
      }
      if (!alive) return;
      drawLive();
      maybeStartPendingWatch();
      maybeStartPendingExternal();
      if (st && torrentDone(st)) {
        torrentPoll.stop();
        torrentPoll = null;
      }
    }, 1000);
  }

  function drawHead() {
    const heading = rel.name || rel.title || 'Раздача';
    title.textContent = heading;
    const to = backTo(rel, store.get('catalog'), store.get('search'));
    const link = h('a', { class: 'back', href: to.href }, icon('chevron_left', 18), to.text);
    if (to.href.startsWith('#/catalog/')) {
      // В раздел каталога — на то же место (спека 11b, 14.3): шаг назад по истории или переход с местом.
      link.addEventListener('click', (e) => {
        if (e.button !== 0 || e.ctrlKey || e.metaKey || e.shiftKey || e.altKey) return; // в новой вкладке — как ссылка
        e.preventDefault();
        returnTo(to.href, ctx.prev);
      });
    }
    back.replaceChildren(link);
    cover.replaceChildren(poster(rel, heading, 'poster big'));
    const desc = rel.description ? h('p', { class: 'desc' }, rel.description) : null;
    fill(info,
      compact ? null : h('h1', null, heading),
      h('div', { class: 'muted sub' }, [rel.original, rel.year || null].filter(Boolean).join(' · ')),
      h('div', { class: 'tags' },
        rel.kinopoisk > 0 ? h('span', { class: 'tag strong' }, 'КП ' + rating(rel.kinopoisk)) : null,
        h('span', { class: 'tag', title: 'Раздающих' }, icon('arrow_upward', 16), String(rel.seeders)),
        rel.quality ? h('span', { class: 'tag' }, rel.quality) : null,
        rel.format ? h('span', { class: rel.preferred ? 'tag pref' : 'tag', title: rel.preferred ? 'Формат в приоритете' : 'Формат файлов' }, rel.format) : null,
        rel.size ? h('span', { class: 'tag' }, size(rel.size)) : null,
        rel.trackerUrl ? h('a', { class: 'tag', href: rel.trackerUrl, target: '_blank', rel: 'noopener' }, 'На трекере', icon('open_in_new', 16)) : null),
      desc || (rel.detailsPending ? h('div', { class: 'lines', 'aria-label': 'Описание загружается' }, h('div', { class: 'skel', style: { width: '80%' } }), h('div', { class: 'skel', style: { width: '55%' } })) : null),
    );
  }

  // files — видеофайлы: после «Скачать» — с прогрессом из движка, до — список из .torrent t1.
  const files = () => (st && st.files && st.files.length ? st.files : (rel && rel.files) || []);
  // downloading — «Скачать» уже нажимали: есть хранимые файлы.
  const downloading = () => !!(st && st.files && st.files.some((f) => f.stored));

  function drawLive() {
    if (!rel) return;
    const fs = files();
    const series = fs.length > 1;
    const names = new Map(shortNames(fs.map((f) => f.name)).map((n, k) => [fs[k].index, n]));
    const label = (f) => (series ? names.get(f.index) : rel.name || rel.title);
    keepFocus(root, () => {
      live.replaceChildren(series ? episodes(fs, label) : '');
      fill(side, panel(fs, label)); // «Следить» у не сериала — null: replaceChildren напечатал бы его
      if (compact) {
        extra.replaceChildren(...side.querySelectorAll('[data-key="follow"], [data-key="remove"], [data-key^="seen-"]'));
      }
    });
    preload.hidden = !pendingWatch && !pendingExternal;
    // Фокус остаётся в текущем навигационном столбце.
    if (focusNext && (!document.activeElement || document.activeElement === document.body)) {
      const el = focusNext === 'watch' ? side.querySelector('[data-key^="watch-"]') : root.querySelector(`[data-key="${focusNext}"]`);
      if (el && !el.disabled) {
        el.focus({ preventScroll: true });
        focusNext = '';
      }
    }
  }

  function episodes(fs, label) {
    const stored = fs.filter((f) => f.stored);
    const total = stored.reduce((n, f) => n + f.size, 0);
    const done = stored.reduce((n, f) => n + (f.done || 0), 0);
    const current = downloading() ? panelFile(fs) : null;
    const groups = fileSeasonGroups(fs);
    const render = (f, n) => {
        const rd = ready[f.readiness || 'none'] || ready.none;
        const on = current && f.index === current.index;
        return h('div', { class: on ? 'ep on' : 'ep', style: on && rd.color ? { borderColor: rd.color } : null },
          h('span', { class: 'num' }, String(n + 1)),
          h('button', { class: 'ep-main', type: 'button', 'data-key': `pick-${f.index}`, 'aria-pressed': String(!!on), onclick: () => pickEpisode(f) },
          h('span', { class: 'ep-title' }, progressOf(f) && progressOf(f).watched ? icon('check', 18, 'просмотрено') : null,
            h('span', { class: 'ep-name', title: f.name }, label(f)), h('span', { class: 'muted small' }, [fileInfo(f), whereStopped(progressOf(f))].filter(Boolean).join(' · '))),
          f.stored ? h('div', { class: 'track' }, h('div', { style: { width: `${f.percent}%`, background: rd.color || 'var(--buffer)' } })) : null,
          positionLine(progressOf(f))),
          watchButton(f, 'btn', 'Смотреть серию ' + (n + 1)));
      };
    const body = groups.length > 1 ? groups.flatMap((g) => [h('h3', { class: 'episode-season' }, g.title), ...g.items.map((f) => render(f, fs.indexOf(f)))])
      : fs.map((f, n) => render(f, n));
    return h('section', { class: 'episodes', 'aria-label': 'Серии' },
      h('div', { class: 'row' }, h('h2', { class: 'grow' }, 'Серии'),
        downloading() ? h('span', { class: 'muted small' }, `${size(done)} из ${size(total)}` + (st.speed > 0 ? ` · ${speed(st.speed)}` : '')) : null),
      body);
  }

  // panelFile — файл панели: выбранный человеком, иначе тот, что продолжать (история устройства), иначе
  // в фокусе загрузки, иначе первый.
  function panelFile(fs) {
    return bestFile(fs, progress, chosen, st && st.focus);
  }

  function panel(fs, label) {
    const out = [];
    const sl = seasonLabel(rel && rel.season) || fileSeasonLabel(fs);
    if (sl) out.push(h('div', { class: 'season-big', 'data-key': 'season' }, sl));
    const f = panelFile(fs);
    const failed = st && st.state === 'error';
    const error = actionError || (st && st.error) || '';
    if (!downloading()) {
      const waiting = st && !failed && !(st.files && st.files.length);
      const target = rel.series || fs.length > 1 ? (fs[0] || f) : f;
      // Списка файлов ещё нет (страница трекера его не отдаёт) — кнопка активна: «Смотреть» добавит
      // раздачу, дождётся списка и запустит первый готовый файл (watchOrPrepare без файла).
      out.push(h('button', { class: 'btn inv big', type: 'button', disabled: busy || waiting, 'data-key': 'watch', 'data-nav-main': true, onclick: () => watchOrPrepare(target) }, icon('play_arrow'), 'Смотреть'),
        // В вебе загрузка — отдельное действие: она ставит раздачу в очередь и не запускает плеер.
        compact ? null : h('button', { class: 'btn', type: 'button', disabled: busy || waiting, 'data-key': 'download-release',
          'aria-label': 'Скачать раздачу', onclick: () => download({}) }, icon('download'), 'Скачать'),
        compact ? null : externalButton(target), followControl());
      if (webBlockedFormat(rel.format)) out.push(h('div', { class: 'muted small' }, icon('warning', 16),
        ` ${rel.format.toUpperCase()} в браузере не играется — «Смотреть» откроет внешний плеер или приложение.`));
      if (waiting) out.push(h('div', { class: 'muted' }, st.state === 'connecting' ? 'Ищем раздающих…' : 'Получаем список файлов…'));
      if (error) out.push(h('div', { class: 'error' }, error));
      return out;
    }
    const rd = ready[f.readiness || 'none'] || ready.none;
    const inFocus = st && f.index === st.focus;
    const p = progressOf(f);
    out.push(h('div', { class: 'panel-title', title: f.name }, label(f)));
    if (whereStopped(p)) out.push(h('div', { class: 'muted' }, whereStopped(p)), positionLine(p));
    const buf = h('div', { class: 'buffer' }, h('div', { class: 'got', style: { width: `${f.percent}%` } }));
    if (inFocus && f.head) buf.append(h('div', { class: 'head', style: { width: `${f.head}%`, background: rd.color } }));
    if (inFocus && f.tail) buf.append(h('div', { class: 'tail', style: { width: `${f.tail}%`, background: rd.color } }));
    out.push(buf, h('div', { class: 'row small' },
      h('span', { class: 'grow' }, `${f.percent} %` + (inFocus && st.speed > 0 ? ` · ${speed(st.speed)}` : '')),
      h('span', { class: 'muted' }, plural(st.peers, 'пир', 'пира', 'пиров'))));
    if (f.readiness === 'wait' && f.waitSec > 0) out.push(h('div', { class: 'eta' }, `Без остановок через ${minutes(f.waitSec)}`));
    out.push(watchButton(f, 'btn big wide'), compact ? null : externalButton(f, 'Во внешнем плеере'), followControl());
    if (ctx.canEdit && rel.hash) {
      const seen = !!(p && p.watched);
      out.push(h('button', { class: 'btn', type: 'button', 'data-key': `seen-${f.index}`, onclick: () => mark(f, !seen) },
        icon(seen ? 'visibility_off' : 'check'), seen ? 'Не просмотрено' : 'Просмотрено'));
      out.push(h('button', { class: 'btn', type: 'button', 'data-key': 'remove', disabled: busy || !removable(st), onclick: remove },
        icon('delete'), 'Удалить'));
    }
    if (error) out.push(h('div', { class: 'error' }, error));
    return out;
  }

  // followControl — «Следить» / «Не следить» под главной кнопкой панели (спека 11b, 6.1).
  function followControl() {
    const b = followButton(rel, ctx.canEdit);
    if (!b) return null;
    return h('button', { class: b.on ? 'btn following' : 'btn', type: 'button', disabled: followBusy, 'data-key': 'follow', 'aria-pressed': String(b.on),
      onclick: () => toggleFollow(b.on) }, icon('notifications'), b.label);
  }

  async function toggleFollow(on) {
    followBusy = true;
    actionError = '';
    drawLive();
    try {
      if (on) await del(`/releases/${id}/follow`);
      else await put(`/releases/${id}/follow`, {});
      rel = { ...rel, follow: on ? '' : 'active' };
    } catch (e) {
      actionError = e.message;
    }
    followBusy = false;
    if (alive) drawLive();
  }

  // mark — «Просмотрено» / «Не просмотрено» у файла панели.
  async function mark(f, watched) {
    try {
      await put(`/history/${rel.hash}/${f.index}`, { watched });
      progress = (await get(`/history/${rel.hash}`)).files;
    } catch (e) {
      actionError = e.message;
    }
    if (alive) drawLive();
  }

  function watchButton(f, cls, label = 'Смотреть') {
    const rd = ready[f.readiness] || ready.none;
    return h('button', { class: cls, type: 'button', disabled: busy, style: { borderColor: rd.color, color: rd.color },
      'data-key': (cls.includes('big') ? 'watch' : 'watch-row') + '-' + f.index, 'data-nav-main': cls.includes('big'), 'aria-label': label + (rd.label ? ' — ' + rd.label : ''), onclick: () => watchOrPrepare(f) },
    icon('play_arrow'), label);
  }

  // externalButton — явный внешний запуск для веб-версии. Он использует тот же серверный
  // контракт, что и обычный просмотр, но не открывает встроенный браузерный плеер.
  function externalButton(f, label = 'Во внешнем плеере') {
    if (!f || !rel) return null;
    return h('button', { class: 'btn', type: 'button', disabled: busy, 'data-key': `external-${f.index}`,
      'aria-label': `${label}: ${f.name || ''}`, onclick: () => externalOrPrepare(f) }, icon('open_in_new'), label);
  }

  function externalOrPrepare(f, fromStart = false) {
    if (!f || busy) return;
    if (f.readiness && f.readiness !== 'none' && rel && rel.hash) {
      externalWatch(f, fromStart);
      return;
    }
    pendingExternal = { index: f.index, fromStart };
    chosen = f.index;
    drawLive();
    download({ file: f.index, prepare: true });
  }

  // pickEpisode — OK на строке серии: до «Скачать» — окно «Скачать «…» — размер?» (замечание № 3 этапа
  // 11b), «Да» — очередь с этой серии; после «Скачать» — файл панели.
  async function pickEpisode(f) {
    if (busy) return;
    chosen = f.index;
    drawLive();
  }

  // watchOrPrepare — единая точка входа для фильма и любой серии. Без файла (списка ещё нет —
  // страница трекера его не отдала): добавить раздачу целиком, первым — фокус сервера.
  function watchOrPrepare(f, fromStart = false) {
    if (busy) return;
    if (!f) {
      pendingWatch = { index: null, fromStart };
      drawLive();
      download({});
      return;
    }
    if (f.readiness && f.readiness !== 'none') { watch(f, fromStart); return; }
    pendingWatch = { index: f.index, fromStart };
    chosen = f.index;
    drawLive();
    download({ file: f.index, prepare: true });
  }

  function maybeStartPendingWatch() {
    if (!pendingWatch || !st || st.state === 'error') {
      if (pendingWatch && st && st.state === 'error') pendingWatch = null;
      return;
    }
    const all = st.files || [];
    const f = pendingWatch.index == null
      ? all.find((item) => item.index === st.focus) || all[0]
      : all.find((item) => item.index === pendingWatch.index);
    if (!f || !f.readiness || f.readiness === 'none') return;
    const next = pendingWatch; pendingWatch = null; watch(f, next.fromStart);
  }

  function maybeStartPendingExternal() {
    if (!pendingExternal || !st || st.state === 'error') {
      if (pendingExternal && st && st.state === 'error') pendingExternal = null;
      return;
    }
    const all = st.files || [];
    const f = all.find((item) => item.index === pendingExternal.index);
    if (!f || !f.readiness || f.readiness === 'none') return;
    const next = pendingExternal;
    pendingExternal = null;
    externalWatch(f, next.fromStart);
  }

  // download — постановка раздачи или файла в очередь. Для отдельной веб-кнопки без параметров
  // ставится вся раздача и плеер не запускается; {prepare} используется кнопками просмотра.
  // {file} — одна серия: удалённую (например, при нехватке места) можно скачать снова; {from} — все
  // серии, первой — эта.
  async function download(what = {}) {
    const fromHere = root.contains(document.activeElement) || what.from !== undefined;
    busy = true;
    actionError = '';
    drawLive();
    try {
      const res = await post(`/releases/${id}/download`, what);
      rel.hash = res.hash;
      if (fromHere && !pendingWatch) focusNext = what.file !== undefined ? 'watch-row-' + what.file : 'watch';
    } catch (e) {
      actionError = e.message;
    }
    busy = false;
    if (!alive) return;
    if (torrentPoll) torrentPoll.now();
    else if (rel.hash) watchTorrent();
    drawLive();
  }

  // remove — «Удалить» (спека 11b, 15.1; № 19): загрузка прерывается, скачанное удаляется — снова «Скачать»;
  // серию с открытым потоком сервер оставляет.
  async function remove() {
    const fromHere = root.contains(document.activeElement);
    if (!(await confirmDialog({ title: removeTitle(rel.name || rel.title, st) })) || !alive) return;
    busy = true;
    actionError = '';
    drawLive();
    try {
      const res = await del(`/downloads/${rel.hash}`);
      if (res && res.skipped) actionError = `Сейчас смотрят — ${plural(res.skipped, 'серия осталась', 'серии остались', 'серий осталось')}`;
      else if (fromHere) focusNext = 'watch';
    } catch (e) {
      actionError = e.message;
    }
    busy = false;
    if (!alive) return;
    if (torrentPoll) torrentPoll.now();
    else watchTorrent();
    drawLive();
  }

  // watch — «Смотреть»: фокус загрузки на этот файл и плеер. На этом ПК — ссылка kinodom://, на
  // других устройствах — .m3u8 (спека этапа 7, раздел 6.3). Форматы, которых браузер не показывает
  // (AVI и пр.), — сразу внешний плеер, веб-плеер не открываем (просьба 2026-10-06).
  async function watch(f, fromStart = false) {
    const to = watchTarget(ctx.status, store.get('moviePlayer'), appBridge(), rel.format);
    const src = `torrent/${rel.hash}/${f.index}`;
    if (to === 'web') {
      ctx.go(playHash(src, fromStart));
      return;
    }
    if (to === 'app' && appBridge().playMovie(src, fromStart)) return;
    busy = true;
    actionError = '';
    chosen = f.index;
    drawLive();
    try {
      const res = await post(`/torrents/${rel.hash}/files/${f.index}/watch`, fromStart ? { fromStart: true } : {});
      if (ctx.local && res.launchUrl) openPlayer(res.launchUrl, res.m3uUrl, ctx.status && ctx.status.protocol);
      else location.href = playerLink(res);
    } catch (e) {
      actionError = e.message;
    }
    busy = false;
    if (!alive) return;
    if (torrentPoll) torrentPoll.now();
    else watchTorrent();
    drawLive();
  }

  // externalWatch — получить актуальную ссылку и открыть внешний плеер. На ПК с
  // зарегистрированным обработчиком используется kinodom://, на остальных устройствах
  // открывается ссылка-плейлист или Android intent.
  async function externalWatch(f, fromStart = false) {
    if (!f || !rel || !rel.hash || busy) return;
    busy = true;
    actionError = '';
    chosen = f.index;
    drawLive();
    try {
      const res = await post(`/torrents/${rel.hash}/files/${f.index}/watch`, fromStart ? { fromStart: true } : {});
      if (ctx.local && res.launchUrl) {
        openPlayer(res.launchUrl, res.m3uUrl, ctx.status && ctx.status.protocol);
      } else {
        const direct = res.play && res.play.url;
        const title = res.play && res.play.title;
        location.href = direct
          ? externalLink({ direct, m3uUrl: res.m3uUrl, title, startSec: res.startSec || 0 }, navigator.userAgent)
          : res.m3uUrl;
      }
    } catch (e) {
      actionError = e.message;
    }
    busy = false;
    if (!alive) return;
    if (torrentPoll) torrentPoll.now();
    else watchTorrent();
    drawLive();
  }

  // drawOthers — «Другие раздачи»: раздачи того же фильма на обоих трекерах (текущая отмечена), под
  // ними — «Искать на трекерах» и состояние трекеров (спека этапа 7, разделы 10.4, 10.5 и 10.7).
  function drawOthers() {
    const items = variants ? variants.items : [];
    const searched = !!(variants && variants.search);
    const out = [];
    if (items.length > 1 || searched) {
      out.push(h('h2', null, 'Другие раздачи'));
      out.push(h('div', { class: 'var-season-list' }, variantSeasonGroups(items).map((g) =>
        h('section', { class: 'var-season', 'aria-label': g.title },
          h('h3', null, g.title), h('div', { class: 'var-list' }, g.items.map(variantRow))))));
    }
    out.push(h('div', { class: 'row gap10' },
      h('button', { class: 'btn', type: 'button', disabled: !!searchPoll, 'data-key': 'search-others', onclick: searchOthers },
        icon('search'), 'Искать на трекерах'),
      searched ? h('div', { class: 'tags' }, trackerTags(variants.search.trackers, items)) : null));
    if (searchError) out.push(h('div', { class: 'error' }, searchError));
    keepFocus(others, () => others.replaceChildren(...out));
  }

  function variantRow(e) {
    const current = String(e.id) === String(id);
    const what = h('span', { class: 'var-what' },
      h('span', { class: 'strong ellipsis' }, [trackerLabel(e.tracker), e.quality].filter(Boolean).join(' · ')),
      h('span', { class: 'muted small ellipsis', title: e.title }, [current ? 'эта раздача' : null, e.season || null].filter(Boolean).join(' · ') || e.name));
    const cells = [what,
      e.format ? formatTag(e.format, e.preferred) : h('span', { class: 'muted', 'aria-label': e.detailsPending ? 'формат загружается' : 'формат неизвестен' }, e.detailsPending ? '…' : '—'),
      h('span', null, size(e.size)),
      h('span', { class: 'seeders' }, icon('arrow_upward', 16), String(e.seeders))];
    return current
      ? h('div', { class: 'var-row on', 'aria-current': 'true' }, ...cells)
      : h('a', { class: 'var-row', href: `#/release/${e.id}`, 'data-key': `var-${e.id}` }, ...cells);
  }

  // searchOthers — «Искать на трекерах»: опрос раз в 1 с, пока поиск идёт (до 30 с), потом раз в 3 с,
  // пока у найденных догружается страница (до 2 минут). Повторные опросы — с poll=1: сервер не ищет
  // заново, даже если трекер ответил ошибкой; новое нажатие — ищет.
  function searchOthers() {
    searchError = '';
    searchStarted = Date.now();
    fetchedAt = 0;
    let repeat = '';
    searchPoll = poll(async () => {
      const done = variants && variants.search && (variants.search.complete || Date.now() - searchStarted > SEARCH_FOR);
      if (done && Date.now() - fetchedAt < 3000) return;
      try {
        variants = await get(`/releases/${id}/variants?search=1${repeat}`);
        repeat = '&poll=1';
        fetchedAt = Date.now();
      } catch (e) {
        searchError = e.message;
        stopSearch();
        return;
      }
      if (!alive) return;
      const loading = variants.items.some((e) => e.detailsPending);
      if ((variants.search.complete && !loading) || Date.now() - searchStarted > FORMATS_FOR) stopSearch();
      else drawOthers();
    }, 1000);
    drawOthers();
  }

  function stopSearch() {
    if (searchPoll) searchPoll.stop();
    searchPoll = null;
    if (alive) drawOthers();
  }

  async function copyLink(f) {
    const name = f.name.split(/[\\/]/).pop();
    let ok = true;
    try {
      await copyText(`${location.origin}/stream/${rel.hash}/${f.index}/${encodeURIComponent(name)}`);
    } catch {
      ok = false;
    }
    copied = { ok, until: Date.now() + COPIED_FOR };
    drawLive();
    setTimeout(() => {
      if (alive) drawLive();
    }, COPIED_FOR + 50);
  }

  // copyLabel — подпись кнопки «Ссылка»: 2 с после копирования — итог.
  function copyLabel() {
    if (copied && Date.now() < copied.until) {
      return copied.ok ? [icon('check'), h('span', { class: 'wide-only' }, 'Скопировано')]
        : [icon('close'), h('span', { class: 'wide-only' }, 'Не скопировалось')];
    }
    return [icon('link'), h('span', { class: 'wide-only' }, 'Ссылка')];
  }

  return () => {
    alive = false;
    releasePoll.stop();
    othersPoll.stop();
    if (torrentPoll) torrentPoll.stop();
    if (searchPoll) searchPoll.stop();
    if (historyPoll) historyPoll.stop();
  };
}

// positionLine — тонкая полоса «где остановились» (не у просмотренного).
function positionLine(p) {
  if (!p || p.watched || !(p.fraction > 0)) return null;
  return h('div', { class: 'track pos-track' }, h('div', { style: { width: `${Math.round(p.fraction * 100)}%`, background: 'var(--text)' } }));
}

// playerLink — плеер на другом устройстве. Android — сразу VLC ссылкой intent: Chrome считает
// скачивание .m3u8 по http небезопасным и просит подтвердить (вживую при подготовке плана 7b);
// нет VLC — Chrome откроет запасной адрес, тот же .m3u8. Остальные устройства — .m3u8.
export function playerLink(res) {
  if (!/Android/i.test(navigator.userAgent)) return res.m3uUrl;
  const u = new URL(res.play.url);
  // l.position — место для VLC, мс (спека этапа 8, раздел 7.3).
  return `intent://${u.host}${u.pathname}#Intent;scheme=${u.protocol.replace(':', '')};type=video/*;package=org.videolan.vlc;`
    + (res.startSec > 0 ? `l.position=${res.startSec * 1000};` : '')
    + `S.title=${encodeURIComponent(res.play.title)};S.browser_fallback_url=${encodeURIComponent(res.m3uUrl)};end`;
}

// finished — всё хранимое скачано: опрашивать больше незачем.
// removable — «Удалить» на странице раздачи (№ 19): есть хранимые файлы, и хотя бы у одного не открыт
// поток (серию, которую смотрят, сервер оставит).
export function removable(st) {
  return !!(st && st.files && st.files.some((f) => f.stored && !f.watching));
}

// removeTitle — подтверждение «Удалить»: сколько скачано из хранимого.
export function removeTitle(name, st) {
  const fs = ((st && st.files) || []).filter((f) => f.stored);
  const total = fs.reduce((n, f) => n + f.size, 0);
  const done = fs.reduce((n, f) => n + Math.min(f.done || 0, f.size), 0);
  const pct = total ? Math.floor((done * 100) / total) : 0;
  return pct >= 100 ? `Удалить «${name}»?` : `Удалить «${name}» — скачано ${pct} %?`;
}

function finished(st) {
  const stored = st.files.filter((f) => f.stored);
  return stored.length > 0 && stored.every((f) => f.readiness === 'done');
}

// torrentDone — опрос загрузки можно остановить: всё хранимое скачано и ни у одного файла не открыт поток —
// иначе «Удалить» у скачанной раздачи застывала бы серой, когда плеер закрыли (финальное ревью 11b-З).
export function torrentDone(st) {
  return finished(st) && !st.files.some((f) => f.stored && f.watching);
}

// fileInfo — строка под названием серии: формат файла, состояние, размер.
function fileInfo(f) {
  const parts = [fileFormat(f.name)];
  if (!f.stored) parts.push(size(f.size));
  else if (f.readiness === 'done') parts.push('скачано', size(f.size));
  else if (f.watching) parts.push('смотрят', `${size(f.done)} из ${size(f.size)}`);
  else if (f.queued) parts.push('в очереди', size(f.size));
  else parts.push(`${size(f.done)} из ${size(f.size)}`);
  return parts.filter(Boolean).join(' · ');
}

// followButton — «Следить» у сериала (только из домашней сети): следят — «Не следить»; закончилась или
// снята — снова «Следить» (спека 11b, 6.1). null — кнопки нет.
export function followButton(rel, canEdit) {
  if (!canEdit || !rel || !rel.series) return null;
  const on = rel.follow === 'active';
  return { label: on ? 'Не следить' : 'Следить', on };
}
