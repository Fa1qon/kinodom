// Раздача: постер, название, теги, описание; до «Скачать» — одна светлая кнопка, после — у каждого
// файла прогресс и «Смотреть» цвета готовности, справа — панель файла в фокусе; ниже — «Другие раздачи»
// фильма и «Искать на трекерах» (спека этапа 7, разделы 5.4, 5.5, 6.3 и 10.7).
import { h, icon, size, speed, rating, minutes, ready, poll, copyText, store, plural, shortNames, keepFocus, fileFormat, openPlayer, confirmDialog, fill } from '../ui.js';
import { get, post, put, del } from '../api.js';
import { whereStopped, resumeIndex } from './history.js';
import { poster, returnTo } from './catalog.js';
import { trackerTags, trackerLabel, backTo } from './search.js';

const PENDING_FOR = 120000; // догрузку страницы раздачи ждём не дольше 2 минут (трекер мог лечь)
const SEARCH_FOR = 30000; // поиск других раздач сервер держит не дольше 30 с
const FORMATS_FOR = 120000; // формат найденных ждём не дольше 2 минут: их страницы догружаются
const MISSING_EVERY = 3000; // раздача не открыта (404) — её могут открыть с другого устройства: проверяем раз в 3 с
const OTHERS_EVERY = 10000; // «Другие раздачи» перечитываются, пока экран открыт
const COPIED_FOR = 2000; // «Скопировано» видно 2 с, перерисовка панели его не сбивает (Х25)

// episodeAction — OK на строке серии (замечание № 3 этапа 11b): до «Скачать» — окно «Скачать?»,
// после — выбрать файл панели; пока идёт действие — ничего.
export function episodeAction(downloadingNow, busyNow) {
  if (busyNow) return 'none';
  return downloadingNow ? 'pick' : 'confirm';
}

export function render(root, r, ctx) {
  const id = r.parts[1];
  let alive = true;
  let rel = null; // /releases/{id}
  let st = null; // /torrents/{hash}; null — раздача не открыта
  let chosen = null; // файл панели, выбранный человеком; null — файл в фокусе
  let actionError = ''; // ошибка «Скачать» или «Смотреть»
  let busy = false;
  let followBusy = false; // «Следить» отправляется
  let torrentPoll = null;
  let descOpen = false; // описание развёрнуто
  let focusNext = ''; // после «Скачать» с пульта ТВ — куда перевести фокус, когда появится «Смотреть»
  const started = Date.now();
  let variants = null; // /releases/{id}/variants — «Другие раздачи»
  let searchPoll = null; // «Искать на трекерах» идёт
  let searchStarted = 0;
  let fetchedAt = 0;
  let searchError = '';
  let progress = []; // /history/{hash} — где остановились на этом устройстве (спека этапа 8, раздел 7.5)
  let historyPoll = null;
  let headKey = ''; // данные заголовка при последней отрисовке: пока догрузка, постер не пересоздаётся каждую секунду
  let missingAt = 0; // когда /torrents/{hash} последний раз ответил 404
  let copied = null; // { ok, until } — подпись кнопки «Ссылка» после копирования

  const back = h('div');
  const cover = h('div', { class: 'rel-cover' });
  const info = h('div', { class: 'rel-info' });
  const live = h('div', { class: 'rel-live' });
  const side = h('aside', { class: 'panel', 'aria-label': 'Просмотр', 'data-nav-column': true });
  const others = h('section', { class: 'others', 'aria-label': 'Другие раздачи' });
  root.append(h('div', { class: 'screen release' }, back,
    h('div', { class: 'rel-grid' }, cover, h('div', { class: 'rel-main', 'data-nav-column': true }, info, live, others), side)));
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
      rel.seeders, rel.quality, rel.format, rel.size, rel.trackerUrl, rel.detailsPending, rel.category]);
    if (key !== headKey) {
      headKey = key;
      drawHead();
    }
    drawLive();
    // Найденное поиском догружается при открытии — опрашиваем, пока страница не загрузится.
    if (!rel.detailsPending || Date.now() - started > PENDING_FOR) releasePoll.stop();
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
      if (st && finished(st)) {
        torrentPoll.stop();
        torrentPoll = null;
      }
    }, 1000);
  }

  function drawHead() {
    const title = rel.name || rel.title || 'Раздача';
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
    cover.replaceChildren(poster(rel, title, 'poster big'));
    const desc = rel.description ? h('p', { class: descOpen ? 'desc' : 'desc clamp' }, rel.description) : null;
    fill(info,
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
      fill(side, panel(fs, label)); // «Следить» у не сериала — null: replaceChildren напечатал бы его
    });
    // Кнопки «Скачать» после удачного нажатия больше нет — фокус пульта ТВ на появившуюся «Смотреть».
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
    return h('section', { class: 'episodes', 'aria-label': 'Серии' },
      h('div', { class: 'row' }, h('h2', { class: 'grow' }, 'Серии'),
        downloading() ? h('span', { class: 'muted small' }, `${size(done)} из ${size(total)}` + (st.speed > 0 ? ` · ${speed(st.speed)}` : '')) : null),
      fs.map((f, n) => {
        const rd = ready[f.readiness || 'none'] || ready.none;
        const on = current && f.index === current.index;
        return h('div', { class: on ? 'ep on' : 'ep', style: on && rd.color ? { borderColor: rd.color } : null },
          h('span', { class: 'num' }, String(n + 1)),
          h('button', { class: 'ep-main', type: 'button', 'data-key': `pick-${f.index}`, 'aria-pressed': String(!!on), onclick: () => pickEpisode(f) },
          h('span', { class: 'ep-title' }, progressOf(f) && progressOf(f).watched ? icon('check', 18, 'просмотрено') : null,
            h('span', { class: 'ep-name', title: f.name }, label(f)), h('span', { class: 'muted small' }, [fileInfo(f), whereStopped(progressOf(f))].filter(Boolean).join(' · '))),
          f.stored ? h('div', { class: 'track' }, h('div', { style: { width: `${f.percent}%`, background: rd.color || 'var(--buffer)' } })) : null,
          positionLine(progressOf(f))),
          f.readiness && f.readiness !== 'none' ? watchButton(f, 'btn')
            : downloading() ? h('button', { class: 'btn', type: 'button', disabled: busy, 'data-key': `get-${f.index}`,
              'aria-label': `Скачать серию ${n + 1}`, onclick: () => download({ file: f.index }) }, icon('download'), 'Скачать') : null);
      }));
  }

  // panelFile — файл панели: выбранный человеком, иначе тот, что продолжать (история устройства), иначе
  // в фокусе загрузки, иначе первый.
  function panelFile(fs) {
    const resume = resumeIndex(fs.map((f) => f.index), progress);
    return fs.find((f) => f.index === chosen) || fs.find((f) => f.index === resume) || (st && fs.find((f) => f.index === st.focus)) || fs[0] || null;
  }

  function panel(fs, label) {
    const out = [];
    const f = panelFile(fs);
    const failed = st && st.state === 'error';
    const error = actionError || (st && st.error) || '';
    if (!downloading()) {
      // До «Скачать»: одна светлая кнопка; после ошибки («нет раздающих», «мало места») — снова она.
      const waiting = st && !failed && !(st.files && st.files.length);
      out.push(h('button', { class: 'btn inv big', type: 'button', disabled: busy || waiting, 'data-key': 'download', 'data-nav-main': true, onclick: () => download() },
        icon('download'), 'Скачать'), followControl());
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
    if (f.readiness && f.readiness !== 'none') {
      out.push(watchButton(f, 'btn big wide'), followControl());
      if (p && !p.watched && p.fraction > 0) {
        out.push(h('button', { class: 'btn', type: 'button', disabled: busy, 'data-key': `start-${f.index}`, onclick: () => watch(f, true) }, icon('history'), 'С начала'));
      }
      out.push(h('div', { class: 'row pair' },
        h('a', { class: 'btn grow wide-only', href: `/m3u/${rel.hash}/${f.index}.m3u8`, download: '', 'data-key': 'm3u' }, icon('playlist_play'), '.m3u8'),
        h('button', { class: 'btn grow', type: 'button', 'data-key': 'copy', 'aria-label': 'Скопировать ссылку на поток', onclick: () => copyLink(f) },
          ...copyLabel())));
    }
    if (!f.readiness || f.readiness === 'none') out.push(followControl());
    if (ctx.canEdit && rel.hash) {
      const seen = !!(p && p.watched);
      out.push(h('button', { class: 'btn', type: 'button', 'data-key': `seen-${f.index}`, onclick: () => mark(f, !seen) },
        icon(seen ? 'visibility_off' : 'check'), seen ? 'Не просмотрено' : 'Просмотрено'));
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

  function watchButton(f, cls) {
    const rd = ready[f.readiness] || ready.none;
    return h('button', { class: cls, type: 'button', disabled: busy, style: { borderColor: rd.color, color: rd.color },
      'data-key': `${cls.includes('big') ? 'watch' : 'watch-row'}-${f.index}`, 'data-nav-main': cls.includes('big'), 'aria-label': `Смотреть — ${rd.label}`, onclick: () => watch(f) },
    icon('play_arrow'), 'Смотреть');
  }

  // pickEpisode — OK на строке серии: до «Скачать» — окно «Скачать «…» — размер?» (замечание № 3 этапа
  // 11b), «Да» — очередь с этой серии; после «Скачать» — файл панели.
  async function pickEpisode(f) {
    const what = episodeAction(downloading(), busy);
    if (what === 'pick') {
      chosen = f.index;
      drawLive();
    }
    if (what !== 'confirm') return;
    const title = rel.name || rel.title || 'раздачу';
    if (await confirmDialog({ title: `Скачать «${title}»` + (rel.size ? ` — ${size(rel.size)}?` : '?') }) && alive) {
      chosen = f.index;
      download({ from: f.index });
    }
  }

  // download — «Скачать»: раздача открывается и становится в очередь (спека этапа 7, раздел 5.5).
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
      if (fromHere) focusNext = what.file !== undefined ? `watch-row-${what.file}` : 'watch';
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
  async function watch(f, fromStart = false) {
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
      h('span', { class: 'strong ellipsis' }, [trackerLabel(e.tracker), e.quality].filter(Boolean).join(' · ')),
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

// followButton — «Следить» у сериала (только из домашней сети): следят — «Не следить»; закончилась или
// снята — снова «Следить» (спека 11b, 6.1). null — кнопки нет.
export function followButton(rel, canEdit) {
  if (!canEdit || !rel || !rel.series) return null;
  const on = rel.follow === 'active';
  return { label: on ? 'Не следить' : 'Следить', on };
}
