// Раздача: постер, название, теги, описание; до «Скачать» — одна светлая кнопка, после — у каждого
// файла прогресс и «Смотреть» цвета готовности, справа — панель файла в фокусе; ниже — «Другие раздачи»
// фильма и «Искать на трекерах» (спека этапа 7, разделы 5.4, 5.5, 6.3 и 10.7).
import { h, icon, size, speed, rating, minutes, ready, poll, copyText, store, plural, shortNames, keepFocus, fileFormat } from '../ui.js';
import { get, post } from '../api.js';
import { poster } from './catalog.js';
import { trackerTags } from './search.js';

const TRACKER = { rutor: 'Rutor', rutracker: 'Rutracker' };
const PENDING_FOR = 120000; // догрузку страницы раздачи ждём не дольше 2 минут (трекер мог лечь)
const SEARCH_FOR = 30000; // поиск других раздач сервер держит не дольше 30 с
const FORMATS_FOR = 120000; // формат найденных ждём не дольше 2 минут: их страницы догружаются

export function render(root, r, ctx) {
  const id = r.parts[1];
  let alive = true;
  let rel = null; // /releases/{id}
  let st = null; // /torrents/{hash}; null — раздача не открыта
  let chosen = null; // файл панели, выбранный человеком; null — файл в фокусе
  let actionError = ''; // ошибка «Скачать» или «Смотреть»
  let busy = false;
  let torrentPoll = null;
  let descOpen = false; // описание развёрнуто
  const started = Date.now();
  let variants = null; // /releases/{id}/variants — «Другие раздачи»
  let searchPoll = null; // «Искать на трекерах» идёт
  let searchStarted = 0;
  let fetchedAt = 0;
  let searchError = '';

  const back = h('div');
  const cover = h('div', { class: 'rel-cover' });
  const info = h('div', { class: 'rel-info' });
  const live = h('div', { class: 'rel-live' });
  const side = h('aside', { class: 'panel', 'aria-label': 'Просмотр' });
  const others = h('section', { class: 'others', 'aria-label': 'Другие раздачи' });
  root.append(h('div', { class: 'screen release' }, back,
    h('div', { class: 'rel-grid' }, cover, h('div', { class: 'rel-main' }, info, live, others), side)));
  get(`/releases/${id}/variants`).then((v) => {
    if (alive && !searchPoll) {
      variants = v;
      drawOthers();
    }
  }, () => {});

  const releasePoll = poll(async () => {
    try {
      rel = await get(`/releases/${id}`);
    } catch (e) {
      releasePoll.stop();
      info.replaceChildren(h('p', { class: 'error' }, e.status === 404 ? 'Такой раздачи нет' : e.message));
      return;
    }
    if (!alive) return;
    drawHead();
    drawLive();
    // Найденное поиском догружается при открытии — опрашиваем, пока страница не загрузится.
    if (!rel.detailsPending || Date.now() - started > PENDING_FOR) releasePoll.stop();
    if (rel.hash && !torrentPoll) watchTorrent();
  }, 1000);

  // watchTorrent — состояние раздачи раз в секунду, пока она качается (спека этапа 7, раздел 4).
  function watchTorrent() {
    torrentPoll = poll(async () => {
      try {
        st = await get(`/torrents/${rel.hash}`);
      } catch (e) {
        st = null;
        if (e.status !== 404) actionError = e.message;
      }
      if (!alive) return;
      drawLive();
      if (!st || finished(st)) {
        torrentPoll.stop();
        torrentPoll = null;
      }
    }, 1000);
  }

  function drawHead() {
    const title = rel.name || rel.title || 'Раздача';
    const saved = store.get('catalog') || '';
    back.replaceChildren(h('a', { class: 'back', href: saved.startsWith(`#/catalog/${rel.tracker}`) ? saved : `#/catalog/${rel.tracker}` },
      icon('chevron_left', 18), [TRACKER[rel.tracker], rel.category].filter(Boolean).join(' · ')));
    cover.replaceChildren(poster(rel, title, 'poster big'));
    const desc = rel.description ? h('p', { class: descOpen ? 'desc' : 'desc clamp' }, rel.description) : null;
    info.replaceChildren(
      h('h1', null, title),
      h('div', { class: 'muted sub' }, [rel.original, rel.year || null].filter(Boolean).join(' · ')),
      h('div', { class: 'tags' },
        rel.kinopoisk > 0 ? h('span', { class: 'tag strong' }, 'КП ' + rating(rel.kinopoisk)) : null,
        h('span', { class: 'tag', title: 'Раздающих' }, icon('arrow_upward', 16), String(rel.seeders)),
        rel.quality ? h('span', { class: 'tag' }, rel.quality) : null,
        rel.format ? h('span', { class: 'tag', title: 'Формат файлов' }, rel.format) : null,
        rel.size ? h('span', { class: 'tag' }, size(rel.size)) : null,
        rel.trackerUrl ? h('a', { class: 'tag', href: rel.trackerUrl, target: '_blank', rel: 'noopener' }, 'На трекере', icon('open_in_new', 16)) : null),
      desc || (rel.detailsPending ? h('div', { class: 'lines', 'aria-label': 'Описание загружается' }, h('div', { class: 'skel', style: { width: '80%' } }), h('div', { class: 'skel', style: { width: '55%' } })) : null),
    );
    // Описания Rutor бывают на экран технических данных: свёрнуто до 8 строк, «Показать полностью» —
    // если не влезло.
    if (desc && !descOpen) {
      requestAnimationFrame(() => {
        if (desc.isConnected && desc.scrollHeight > desc.clientHeight + 2) {
          desc.after(h('button', { class: 'link', type: 'button', onclick: () => {
            descOpen = true;
            drawHead();
          } }, 'Показать полностью'));
        }
      });
    }
  }

  // files — видеофайлы: после «Скачать» — с прогрессом из движка, до — список из .torrent Rutor.
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
      side.replaceChildren(...panel(fs, label));
    });
  }

  function episodes(fs, label) {
    const stored = fs.filter((f) => f.stored);
    const total = stored.reduce((n, f) => n + f.size, 0);
    const done = stored.reduce((n, f) => n + (f.done || 0), 0);
    const current = downloading() ? panelFile(fs) : null;
    return h('section', { class: 'episodes', 'aria-label': 'Серии' },
      h('div', { class: 'row' }, h('h2', { class: 'grow' }, 'Серии'),
        downloading() ? h('span', { class: 'muted small' }, `${size(done)} из ${size(total)}` + (st.speed > 0 ? ` · ${speed(st.speed)}` : '')) : null),
      fs.map((f, n) => {
        const rd = ready[f.readiness || 'none'] || ready.none;
        const on = current && f.index === current.index;
        return h('div', { class: on ? 'ep on' : 'ep', style: on && rd.color ? { borderColor: rd.color } : null },
          h('span', { class: 'num' }, String(n + 1)),
          h('button', { class: 'ep-main', type: 'button', 'data-key': `pick-${f.index}`, 'aria-pressed': String(!!on), onclick: () => {
            chosen = f.index;
            drawLive();
          } },
          h('span', { class: 'ep-title' }, h('span', { class: 'ep-name', title: f.name }, label(f)), h('span', { class: 'muted small' }, fileInfo(f))),
          f.stored ? h('div', { class: 'track' }, h('div', { style: { width: `${f.percent}%`, background: rd.color || 'var(--buffer)' } })) : null),
          f.readiness && f.readiness !== 'none' ? watchButton(f, 'btn')
            : downloading() ? h('button', { class: 'btn', type: 'button', disabled: busy, 'data-key': `get-${f.index}`,
              'aria-label': `Скачать серию ${n + 1}`, onclick: () => download(f.index) }, icon('download'), 'Скачать') : null);
      }));
  }

  // panelFile — файл панели: выбранный человеком, иначе в фокусе загрузки, иначе первый.
  function panelFile(fs) {
    return fs.find((f) => f.index === chosen) || (st && fs.find((f) => f.index === st.focus)) || fs[0] || null;
  }

  function panel(fs, label) {
    const out = [];
    const f = panelFile(fs);
    const failed = st && st.state === 'error';
    const error = actionError || (st && st.error) || '';
    if (!downloading()) {
      // До «Скачать»: одна светлая кнопка; после ошибки («нет раздающих», «мало места») — снова она.
      const waiting = st && !failed && !(st.files && st.files.length);
      out.push(h('button', { class: 'btn inv big', type: 'button', disabled: busy || waiting, 'data-key': 'download', onclick: download },
        icon('download'), 'Скачать'));
      if (waiting) out.push(h('div', { class: 'muted' }, st.state === 'connecting' ? 'Ищем раздающих…' : 'Получаем список файлов…'));
      if (error) out.push(h('div', { class: 'error' }, error));
      return out;
    }
    const rd = ready[f.readiness || 'none'] || ready.none;
    const inFocus = st && f.index === st.focus;
    out.push(h('div', { class: 'panel-title', title: f.name }, label(f)));
    const buf = h('div', { class: 'buffer' }, h('div', { class: 'got', style: { width: `${f.percent}%` } }));
    if (inFocus && f.head) buf.append(h('div', { class: 'head', style: { width: `${f.head}%`, background: rd.color } }));
    if (inFocus && f.tail) buf.append(h('div', { class: 'tail', style: { width: `${f.tail}%`, background: rd.color } }));
    out.push(buf, h('div', { class: 'row small' },
      h('span', { class: 'grow' }, `${f.percent} %` + (inFocus && st.speed > 0 ? ` · ${speed(st.speed)}` : '')),
      h('span', { class: 'muted' }, plural(st.peers, 'пир', 'пира', 'пиров'))));
    if (f.readiness === 'wait' && f.waitSec > 0) out.push(h('div', { class: 'eta' }, `Без остановок через ${minutes(f.waitSec)}`));
    if (f.readiness && f.readiness !== 'none') {
      out.push(watchButton(f, 'btn big wide'));
      out.push(h('div', { class: 'row pair' },
        h('a', { class: 'btn grow wide-only', href: `/m3u/${rel.hash}/${f.index}.m3u8`, download: '', 'data-key': 'm3u' }, icon('playlist_play'), '.m3u8'),
        h('button', { class: 'btn grow', type: 'button', 'data-key': 'copy', 'aria-label': 'Скопировать ссылку на поток', onclick: (e) => copyLink(f, e.currentTarget) },
          icon('link'), h('span', { class: 'wide-only' }, 'Ссылка'))));
    }
    if (error) out.push(h('div', { class: 'error' }, error));
    return out;
  }

  function watchButton(f, cls) {
    const rd = ready[f.readiness] || ready.none;
    return h('button', { class: cls, type: 'button', disabled: busy, style: { borderColor: rd.color, color: rd.color },
      'data-key': `${cls.includes('big') ? 'watch' : 'watch-row'}-${f.index}`, 'aria-label': `Смотреть — ${rd.label}`, onclick: () => watch(f) },
    icon('play_arrow'), 'Смотреть');
  }

  // download — «Скачать»: раздача открывается и становится в очередь (спека этапа 7, раздел 5.5).
  // file — одна серия: удалённую (например, при нехватке места) можно скачать снова.
  async function download(file) {
    busy = true;
    actionError = '';
    drawLive();
    try {
      const res = await post(`/releases/${id}/download`, typeof file === 'number' ? { file } : {});
      rel.hash = res.hash;
    } catch (e) {
      actionError = e.message;
    }
    busy = false;
    if (!alive) return;
    if (torrentPoll) torrentPoll.now();
    else if (rel.hash) watchTorrent();
    drawLive();
  }

  // watch — «Смотреть»: фокус загрузки на этот файл и плеер. На этом ПК — ссылка kinodom://, на
  // других устройствах — .m3u8 (спека этапа 7, раздел 6.3).
  async function watch(f) {
    busy = true;
    actionError = '';
    chosen = f.index;
    drawLive();
    try {
      const res = await post(`/torrents/${rel.hash}/files/${f.index}/watch`);
      location.href = ctx.local && res.launchUrl ? res.launchUrl : playerLink(res);
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
      out.push(h('div', { class: 'var-list' }, items.map(variantRow)));
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
      h('span', { class: 'strong ellipsis' }, [TRACKER[e.tracker] || e.tracker, e.quality].filter(Boolean).join(' · ')),
      h('span', { class: 'muted small ellipsis', title: e.title }, [current ? 'эта раздача' : null, e.season || null].filter(Boolean).join(' · ') || e.name));
    const cells = [what,
      e.format ? h('span', null, e.format) : h('span', { class: 'muted', 'aria-label': e.detailsPending ? 'формат загружается' : 'формат неизвестен' }, e.detailsPending ? '…' : '—'),
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

  async function copyLink(f, button) {
    const name = f.name.split(/[\\/]/).pop();
    try {
      await copyText(`${location.origin}/stream/${rel.hash}/${f.index}/${encodeURIComponent(name)}`);
      button.replaceChildren(icon('check'), h('span', { class: 'wide-only' }, 'Скопировано'));
    } catch {
      button.replaceChildren(icon('close'), h('span', { class: 'wide-only' }, 'Не скопировалось'));
    }
  }

  return () => {
    alive = false;
    releasePoll.stop();
    if (torrentPoll) torrentPoll.stop();
    if (searchPoll) searchPoll.stop();
  };
}

// playerLink — плеер на другом устройстве. Android — сразу VLC ссылкой intent: Chrome считает
// скачивание .m3u8 по http небезопасным и просит подтвердить (вживую при подготовке плана 7b);
// нет VLC — Chrome откроет запасной адрес, тот же .m3u8. Остальные устройства — .m3u8.
function playerLink(res) {
  if (!/Android/i.test(navigator.userAgent)) return res.m3uUrl;
  const u = new URL(res.play.url);
  return `intent://${u.host}${u.pathname}#Intent;scheme=${u.protocol.replace(':', '')};type=video/*;package=org.videolan.vlc;`
    + `S.title=${encodeURIComponent(res.play.title)};S.browser_fallback_url=${encodeURIComponent(res.m3uUrl)};end`;
}

// finished — всё хранимое скачано: опрашивать больше незачем.
function finished(st) {
  const stored = st.files.filter((f) => f.stored);
  return stored.length > 0 && stored.every((f) => f.readiness === 'done');
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
