// Полноэкранный плеер канала (план 2026-10-06, Task A): тот же движок, что у окна проверки источников
// (preview.attach — HLS через hls.js или браузер, MPEG-TS через mpegts.js), источники канала по порядку
// с автопереходом к следующему при ошибке. Список каналов (запомненный экрана «Каналы», иначе «Все»):
// ↑/↓ — соседний канал, Enter — список поверх видео, Esc — назад. Под видео — текущая передача и
// метрики (размер кадра, время до кадра, подвисания, задержка, запас). DASH браузер не играет —
// сообщение и кнопка внешнего запуска.
import { h, icon } from '../ui.js';
import { get } from '../api.js';
import { attach, playerKind, watchURL, previewStats } from './preview.js';
import { hhmm, logo, listFor, channelPlayerLink } from './tvkit.js';

export function render(root, r) {
  const startKey = r.parts[1];
  const video = h('video', { class: 'pl-video', controls: true, autoplay: true, playsinline: true });
  const load = h('div', { class: 'pl-load', role: 'status', hidden: true }, icon('progress_activity'), 'Загрузка канала');
  const title = h('div', { class: 'pl-title' }, 'Канал');
  const zone = h('span', { class: 'tag', hidden: true });
  const back = h('button', { class: 'btn pl-back', type: 'button', 'data-key': 'cp-back', onclick: leave }, icon('arrow_back'), 'Назад');
  const listBtn = h('button', { class: 'sq pl-btn', type: 'button', 'aria-label': 'Каналы', title: 'Каналы', 'data-key': 'cp-list', onclick: togglePanel }, icon('playlist_play'));
  const now = h('div', { class: 'cp-now' });
  const stats = h('div', { class: 'cp-stats muted small', role: 'status' });
  const msg = h('div', { class: 'pl-box', hidden: true });
  const panel = h('div', { class: 'cp-panel', role: 'listbox', 'aria-label': 'Каналы', hidden: true });
  const ui = h('div', { class: 'pl-ui' },
    h('div', { class: 'pl-top' }, back, title, zone, h('div', { class: 'grow' }), listBtn),
    h('div', { class: 'pl-bottom' }, now, stats));
  const wrap = h('div', { class: 'player channel-player' }, video, load, ui, panel, msg);
  root.append(wrap);

  let list = []; // {key, version, name, number, logo, versionLabel, versionCount}
  let idx = -1;
  let card = null;
  let session = null; // {destroy, latency(), buffer()} — текущий источник
  let gen = 0; // номер запуска источника: ошибка прежнего не перебивает новый (быстрые ↑/↓)
  let srcIdx = 0;
  let alive = true;
  let started = 0; // плеер подключён: «до кадра» меряется от него
  const s = { w: 0, h: 0, firstMs: 0, stalls: 0, latency: 0, buffer: 0 };
  let timer = 0;
  let clock = 0;
  let sel = 0; // выбор в панели списка

  const item = (c) => ({ key: c.key, version: c.version || c.key, name: c.name || '', number: c.number || 0,
    logo: c.logo || '', versionLabel: c.versionLabel || '', versionCount: c.versionCount || 1 });

  const stopPlayer = () => {
    gen++;
    if (session) session.destroy();
    session = null;
    video.removeEventListener('playing', onPlaying);
    video.removeEventListener('waiting', onWaiting);
    video.removeAttribute('src');
    video.load();
  };

  const stopTimers = () => {
    clearInterval(timer);
    clearInterval(clock);
    timer = clock = 0;
  };

  function leave() {
    location.hash = '#/channels';
  }

  // apiPath — карточка канала по его записи в списке: ключ канала, версия — ?version= (она же ключ «Смотреть»).
  const apiPath = (it) => '/channels/' + encodeURIComponent(it.key) +
    (it.version && it.version !== it.key ? '?version=' + encodeURIComponent(it.version) : '');

  async function loadList() {
    const stored = listFor(startKey);
    if (stored) {
      list = stored.list;
    } else {
      const res = await get('/channels');
      list = (res.channels || []).map(item);
    }
    let i = list.findIndex((x) => x.version === startKey || x.key === startKey);
    if (i < 0) {
      list.unshift(item({ key: startKey, version: startKey, name: 'Канал' }));
      i = 0;
    }
    await loadChannel(i);
  }

  async function loadChannel(i) {
    idx = ((i % list.length) + list.length) % list.length;
    sel = idx;
    const wanted = list[idx].version;
    stopPlayer();
    stopTimers();
    srcIdx = 0;
    msg.hidden = true;
    load.hidden = false;
    stats.textContent = '';
    s.w = s.h = s.firstMs = s.stalls = s.latency = s.buffer = 0;
    try {
      card = await get(apiPath(list[idx]));
    } catch (e) {
      if (!alive || list[idx].version !== wanted) return; // пока грузили, ушли на другой канал
      load.hidden = true;
      return fail('Канал не открылся: ' + e.message);
    }
    if (!alive || list[idx].version !== wanted) return;
    title.textContent = card.name || 'Канал';
    zone.hidden = !(card.versionCount > 1 && card.versionLabel);
    zone.textContent = card.versionLabel || '';
    drawNow();
    startSource();
  }

  function drawNow() {
    const p = card && card.now;
    const n = card && card.next;
    now.textContent = p
      ? `${hhmm(p.start, card.utcOffset)}–${hhmm(p.stop, card.utcOffset)} ${p.title}${n ? ' · далее ' + n.title : ''}`
      : 'Программы нет';
  }

  // playable — источники для плеера по порядку: предложенные и живые; играть можно HLS и MPEG-TS.
  const playable = () => (card.sources || []).filter((x) => x.offered !== false && x.state === 'alive' && x.id &&
    playerKind(x) !== 'dash');
  const dashOnly = () => !(card.sources || []).some((x) => x.offered !== false && x.state === 'alive' && playerKind(x) !== 'dash') &&
    (card.sources || []).some((x) => x.offered !== false && playerKind(x) === 'dash');

  function startSource() {
    stopPlayer();
    stopTimers();
    const g = gen;
    const all = playable();
    if (srcIdx >= all.length) {
      load.hidden = true;
      if (dashOnly()) {
        msg.replaceChildren(
          h('p', null, 'Этот канал в браузере не показывается — поток DASH.'),
          h('div', { class: 'row gap10' },
            h('button', { class: 'btn inv', type: 'button', 'data-key': 'cp-external', onclick: openExternal }, icon('open_in_new'), 'Открыть во внешнем плеере'),
            h('button', { class: 'btn', type: 'button', 'data-key': 'cp-back2', onclick: leave }, icon('arrow_back'), 'Назад')));
        msg.hidden = false;
      } else {
        fail('Рабочих источников нет — канал сейчас не показывает');
      }
      return;
    }
    const src = all[srcIdx];
    load.hidden = false;
    started = performance.now();
    video.addEventListener('playing', onPlaying);
    video.addEventListener('waiting', onWaiting);
    attach(video, watchURL(src.id), playerKind(src), (e) => {
      if (g === gen) onFail(e);
    }).then((p) => {
      if (!alive || g !== gen) {
        p.destroy();
        return;
      }
      session = p;
      video.play().catch(() => { load.hidden = true; }); // без касания браузер может отказать — кнопка ▶ на видео
    }, (e) => {
      if (alive && g === gen) onFail({ what: 'net', text: e.message });
    });
    clock = setInterval(refreshCard, 60000);
  }

  function onPlaying() {
    if (!s.firstMs && started) s.firstMs = performance.now() - started;
    load.hidden = true;
    msg.hidden = true;
  }

  function onWaiting() {
    if (s.firstMs) s.stalls++;
  }

  function drawStats() {
    if (!session) return;
    s.w = video.videoWidth;
    s.h = video.videoHeight;
    s.latency = session.latency();
    s.buffer = session.buffer();
    stats.textContent = previewStats(s);
  }

  // refreshCard — раз в минуту: передача и название; текущий источник пропал — плеер перебирает заново.
  async function refreshCard() {
    if (!card || !session) return;
    try {
      const fresh = await get(apiPath(list[idx]));
      if (!alive) return;
      card = fresh;
      title.textContent = card.name || 'Канал';
      drawNow();
      const cur = playable()[srcIdx];
      if (cur && !(card.sources || []).some((x) => x.id === cur.id && x.state === 'alive' && x.offered !== false)) {
        srcIdx = 0;
        startSource();
      }
    } catch {
      // минутный опрос не критичен
    }
  }

  function onFail(e) {
    stopPlayer();
    stopTimers();
    srcIdx++;
    if (card && playable().length > srcIdx) {
      startSource();
      return;
    }
    load.hidden = true;
    fail((e && e.text) || 'Источник не открылся');
  }

  function fail(text) {
    msg.replaceChildren(
      h('p', null, text),
      h('div', { class: 'row gap10' },
        h('button', { class: 'btn inv', type: 'button', 'data-key': 'cp-retry', onclick: () => {
          msg.hidden = true;
          load.hidden = false;
          if (card) { srcIdx = 0; startSource(); } else loadChannel(idx);
        } }, icon('refresh'), 'Повторить'),
        h('button', { class: 'btn', type: 'button', 'data-key': 'cp-back3', onclick: leave }, icon('arrow_back'), 'Назад')));
    msg.hidden = false;
  }

  async function openExternal() {
    try {
      const res = await get('/channels/' + encodeURIComponent(card.version) + '/play');
      location.href = channelPlayerLink(res);
    } catch (e) {
      fail(e.message);
    }
  }

  // Панель списка каналов.
  function togglePanel() {
    if (panel.hidden) openPanel();
    else closePanel();
  }

  function openPanel() {
    drawPanel();
    panel.hidden = false;
    focusItem(sel);
  }

  function closePanel() {
    panel.hidden = true;
  }

  function drawPanel() {
    panel.replaceChildren(...list.map((c, i) => {
      const cur = i === idx;
      return h('div', {
        class: 'cp-item' + (cur ? ' on' : ''), role: 'option', tabindex: '-1', 'data-key': 'cp-ch-' + i,
        'aria-selected': String(cur), onclick: () => pickChannel(i),
      },
        c.number ? h('span', { class: 'cp-num' }, String(c.number)) : null,
        logo(c), h('span', { class: 'cp-name' }, c.name, c.versionCount > 1 && c.versionLabel ? h('span', { class: 'tag' }, c.versionLabel) : null));
    }));
  }

  function pickChannel(i) {
    closePanel();
    if (i !== idx) loadChannel(i);
  }

  function focusItem(i) {
    const el = panel.children[i];
    if (el) el.focus({ preventScroll: true });
  }

  // Клавиши и пульт ТВ: ↑/↓ — каналы (или пункты списка), Enter — список/выбор, Esc и Backspace — назад.
  function onKey(e) {
    if (e.altKey || e.ctrlKey || e.metaKey) return;
    const take = () => {
      e.preventDefault();
      e.stopPropagation();
    };
    const panelOpen = !panel.hidden;
    const boxOpen = !msg.hidden;
    if (e.key === 'ArrowUp' || e.key === 'ArrowDown') {
      take();
      const dir = e.key === 'ArrowUp' ? -1 : 1;
      if (panelOpen) {
        sel = ((sel + dir) % list.length + list.length) % list.length;
        focusItem(sel);
        if (panel.children[sel]) panel.children[sel].scrollIntoView({ block: 'nearest' });
      } else if (!boxOpen) {
        loadChannel(idx + dir);
      }
      return;
    }
    if (e.key === 'Enter') {
      if (boxOpen) return; // по кнопкам окна — nav.js
      take();
      if (panelOpen) pickChannel(sel);
      else openPanel();
      return;
    }
    if (e.key === 'Escape' || e.key === 'Backspace') {
      take();
      if (panelOpen) closePanel();
      else leave();
    }
  }

  document.addEventListener('keydown', onKey, true);

  load.hidden = false;
  loadList().catch((e) => {
    if (alive) {
      load.hidden = true;
      fail(e.message);
    }
  });

  return () => {
    alive = false;
    document.removeEventListener('keydown', onKey, true);
    stopTimers();
    stopPlayer();
  };
}
