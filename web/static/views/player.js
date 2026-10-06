// Плеер фильмов в браузере (спека цикла 18, раздел 5; план 18Б): поток сервера (MPEG-TS, mpegts.js) с ключевого
// кадра, своя шкала на весь файл, озвучка и субтитры, точное место — серверу, следующая серия.
import { h, icon, store, openPlayer } from '../ui.js';
import { get } from '../api.js';
import { loadLib } from './preview.js';
import * as P from './player-parts.js';

export function render(root, r, ctx) {
  const src = P.srcOf(r.parts.slice(1));
  const fromStart = r.query.get('fromStart') === '1';
  const sid = P.newSid();
  let alive = true;
  let info = null; // ответ /play/{src}
  let mpegts = null;
  let mp = null; // плеер mpegts.js текущего потока
  let k = 0; // ключевой кадр начала потока, с от начала файла
  let audio = null;
  let sub = null;
  let subAbort = null;
  let starting = 0; // номер запуска: ответ ключевого кадра старого запуска не перебивает новый
  let retried = false; // поток уже перезапускали после обрыва
  let seek = null; // накопленная перемотка стрелками
  let seekTimer = 0;
  let lastReport = 0;
  let lastActivity = Date.now();
  let menu = null; // открытое меню: 'audio' или 'subs'
  let dragging = false;
  let nextTimer = 0;
  let parkTimer = 0;
  let parkedAt = -1; // поток остановлен на долгой паузе — место, с которого продолжить
  let tapTimer = 0;
  let lastTouch = 0; // последнее касание: мышиные события вслед за ним — не мышь
  let lastStat = 0; // последняя статистика mpegts.js: нет дольше STALL_MS при просмотре — загрузка встала
  let pendingAt = -1; // поток с этого места запускается
  let played = false; // что-то посмотрели — можно сообщать место
  let lastTap = 0;

  const video = h('video', { class: 'pl-video', playsinline: true });
  const track = video.addTextTrack('subtitles', 'Субтитры', 'ru');
  track.mode = 'showing';
  const load = h('div', { class: 'pl-load', role: 'status' }, icon('progress_activity'), 'Загрузка');
  const title = h('div', { class: 'pl-title' });
  const bar = h('input', { class: 'pl-bar', type: 'range', min: '0', max: '0', step: '1', value: '0', 'aria-label': 'Место', 'data-key': 'pl-bar' });
  const time = h('div', { class: 'pl-time' }, '0:00 / 0:00');
  const btn = (name, label, key, onclick) => h('button', { class: 'sq pl-btn', type: 'button', 'aria-label': label, title: label, 'data-key': key, onclick }, icon(name));
  const playBtn = btn('play_arrow', 'Смотреть', 'pl-play', () => togglePause());
  const audioBtn = btn('audiotrack', 'Озвучка', 'pl-audio', () => openMenu('audio'));
  const subsBtn = btn('subtitles', 'Субтитры', 'pl-subs', () => openMenu('subs'));
  const prevBtn = btn('skip_previous', 'Предыдущая серия', 'pl-prev', () => goPrev());
  const nextBtn = btn('skip_next', 'Следующая серия', 'pl-next', () => goNext());
  const muteBtn = btn('volume_up', 'Звук', 'pl-mute', () => toggleMute());
  const fullBtn = btn('fullscreen', 'Во весь экран', 'pl-full', () => toggleFull());
  const back10Btn = btn('fast_rewind', 'Назад на 10 с', 'pl-back10', () => jump(-10));
  const fwd30Btn = btn('fast_forward', 'Вперёд на 30 с', 'pl-fwd30', () => jump(30));
  const backBtn = h('button', { class: 'btn pl-back', type: 'button', 'data-key': 'pl-back', onclick: () => leave() }, icon('arrow_back'), 'Назад');
  const menuBox = h('div', { class: 'pl-menu', role: 'menu', hidden: true });
  const nextBox = h('div', { class: 'pl-box', hidden: true });
  const msg = h('div', { class: 'pl-box', hidden: true });
  const ui = h('div', { class: 'pl-ui' },
    h('div', { class: 'pl-top' }, backBtn, title),
    h('div', { class: 'pl-bottom' }, bar,
      h('div', { class: 'pl-row' }, prevBtn, playBtn, back10Btn, fwd30Btn,
        time, h('div', { class: 'grow' }), audioBtn, subsBtn, nextBtn, muteBtn, fullBtn)));
  const wrap = h('div', { class: 'player' }, video, load, ui, menuBox, nextBox, msg);
  root.append(wrap);
  audioBtn.hidden = subsBtn.hidden = prevBtn.hidden = nextBtn.hidden = true;

  const vol = Number(store.get('player.volume'));
  if (store.get('player.volume') !== null && vol >= 0 && vol <= 1) video.volume = vol;
  video.muted = store.get('player.muted') === '1';
  muteBtn.replaceChildren(icon(video.muted ? 'volume_off' : 'volume_up'));

  const dur = () => (info ? info.durationSec : 0);
  const pos = () => P.positionNow({ parkedAt, pendingAt, k, currentTime: video.currentTime });

  async function begin() {
    if (!src) return fail('Такого файла нет', false);
    try {
      info = await get(`/play/${src}` + (fromStart ? '?fromStart=1' : ''));
    } catch (e) {
      return fail(e.message, false);
    }
    if (!alive) return;
    title.textContent = info.title;
    bar.max = String(Math.floor(dur()));
    audioBtn.hidden = info.audio.length < 2;
    subsBtn.hidden = P.textSubs(info.subs).length === 0;
    prevBtn.hidden = !info.prev;
    nextBtn.hidden = !info.next;
    const mse = window.MediaSource && MediaSource.isTypeSupported.bind(MediaSource);
    if (!P.canShow(info.video.mime, mse)) return fail('Браузер не покажет этот файл', true);
    try {
      mpegts = await loadLib('vendor/mpegts.js', 'mpegts');
    } catch (e) {
      return fail(e.message, true);
    }
    if (!alive) return;
    if (!mpegts.isSupported()) return fail('Браузер не покажет этот файл', true);
    audio = P.pickTrack(info.audio, P.readMem(store.get(P.memKey(info.hash, 'audio'))));
    sub = P.pickSub(info.subs, P.readMem(store.get(P.memKey(info.hash, 'subs'))));
    start(info.startSec);
  }

  // start — поток с ключевого кадра не позже at.
  async function start(at) {
    const n = ++starting;
    stop();
    parkedAt = -1;
    pendingAt = Math.max(at, 0);
    showTime(pendingAt);
    msg.hidden = true;
    load.hidden = false;
    let t = 0;
    if (at > 0) {
      try {
        t = (await get(`/play/${src}/keyframe?t=${Math.floor(at)}`)).t;
      } catch (e) {
        if (alive && n === starting) fail(e.message, true);
        return;
      }
    }
    if (!alive || n !== starting) return;
    k = t;
    pendingAt = -1;
    showTime(k);
    // Позади держим 10–30 с (по умолчанию 2–3 мин — лишнее место в MSE, ревью 18Б).
    mp = mpegts.createPlayer({ type: 'mpegts', isLive: false, url: P.streamURL(src, k, audio, sid) },
      { enableWorker: false, enableStashBuffer: false, lazyLoad: false, autoCleanupSourceBuffer: true,
        autoCleanupMaxBackwardDuration: 30, autoCleanupMinBackwardDuration: 10 });
    mp.on(mpegts.Events.ERROR, (type, detail, data) => onError(data));
    mp.on(mpegts.Events.STATISTICS_INFO, () => { lastStat = Date.now(); });
    lastStat = Date.now();
    mp.attachMediaElement(video);
    mp.load();
    video.play().catch(() => {
      if (!alive || n !== starting) return; // старый запуск — не трогать новый
      // Без жеста пользователя со звуком не играет: «Загрузка» прочь, кнопка — «Смотреть», долгая пауза — как у паузы
      // (иначе поток копится в браузере, ревью 18Б, I1).
      load.hidden = true;
      syncPlayBtn();
      armPark();
      showUI();
    });
    startSubs();
  }

  function syncPlayBtn() {
    const b = P.playButton(video.paused || !mp);
    playBtn.replaceChildren(icon(b.icon));
    playBtn.setAttribute('aria-label', b.label);
    playBtn.title = b.label;
  }

  function armPark() {
    clearTimeout(parkTimer);
    parkTimer = setTimeout(park, P.PARK_AFTER);
  }

  function stop() {
    clearTimeout(parkTimer);
    if (subAbort) {
      subAbort.abort();
      subAbort = null;
    }
    if (!mp) return;
    const p = mp;
    mp = null;
    try {
      p.pause();
      p.unload();
      p.detachMediaElement();
    } catch {
      // уже закрыт
    } finally {
      p.destroy();
    }
  }

  function clearCues() {
    for (const c of Array.from(track.cues || [])) track.removeCue(c);
  }

  // startSubs — субтитры с кадра k: реплики во времени файла — в дорожку минус k.
  async function startSubs() {
    if (subAbort) subAbort.abort();
    subAbort = null;
    clearCues();
    if (!sub || !info) return;
    const ac = new AbortController();
    subAbort = ac;
    const at = k;
    try {
      const resp = await fetch(P.subsURL(src, sub, at, sid), { signal: ac.signal });
      if (!resp.ok || !resp.body) return;
      const reader = resp.body.getReader();
      const dec = new TextDecoder();
      const parser = P.cueParser();
      for (;;) {
        const { value, done } = await reader.read();
        for (const c of parser.push(done ? '' : dec.decode(value, { stream: true }), done)) {
          if (c.end - at > 0) track.addCue(new VTTCue(Math.max(c.start - at, 0), c.end - at, c.text));
        }
        if (done) break;
      }
    } catch {
      // перемотали, сменили субтитры или ушли — новый поток субтитров уже идёт
    }
  }

  function onError(data) {
    report(); // место — до перезапуска или сообщения (ревью 18Б, I4)
    if (data && data.code === 429) return fail('Сейчас смотрят на трёх устройствах — закройте один плеер', true);
    if (!retried) {
      retried = true;
      start(pos());
      return;
    }
    fail('Поток оборвался', true);
  }

  function fail(text, external) {
    stop();
    load.hidden = true;
    msg.replaceChildren(h('div', { class: 'pl-msg-text' }, text), h('div', { class: 'row gap10' },
      external && info ? h('button', { class: 'btn inv', type: 'button', 'data-key': 'pl-external', onclick: openExternal }, icon('open_in_new'), 'Открыть во внешнем плеере') : null,
      h('button', { class: 'btn', type: 'button', 'data-key': 'pl-leave', onclick: () => leave() }, icon('arrow_back'), 'Назад')));
    msg.hidden = false;
    msg.querySelector('button').focus({ preventScroll: true });
  }

  // openExternal — внешний плеер с того места, где оборвалось: место — серверу, ссылки — свежие (в первых сведениях — место
  // открытия страницы, ревью 18Б, I4).
  async function openExternal() {
    let at = info;
    try {
      await report();
      at = await get(`/play/${src}`);
    } catch {
      // сервер не ответил — ссылки первых сведений
    }
    if (ctx.local && at.launchUrl) openPlayer(at.launchUrl, at.m3uUrl, ctx.status && ctx.status.protocol);
    else location.href = P.externalLink(at, navigator.userAgent);
  }

  function showTime(t) {
    if (!dragging) bar.value = String(Math.floor(t));
    time.textContent = `${P.fmtTime(t)} / ${P.fmtTime(dur())}`;
  }

  function jump(delta) {
    if (!info) return;
    seek = P.seekStep(seek, delta, Date.now(), pos());
    showUI();
    showTime(P.clampPos(seek.target, dur()));
    clearTimeout(seekTimer);
    seekTimer = setTimeout(() => {
      const t = P.clampPos(seek.target, dur());
      seek = null;
      seekTo(t);
    }, P.SEEK_GAP);
  }

  function seekTo(t) {
    report();
    retried = false;
    start(P.clampPos(t, dur()));
  }

  // report — место серверу (спека 18, раздел 4).
  function report(final = false) {
    if (!info || !(dur() > 0) || !P.shouldReport(played, final) || (!mp && parkedAt < 0 && pendingAt < 0 && !final)) return Promise.resolve();
    lastReport = Date.now();
    const body = P.reportBody(final ? dur() : pos(), dur());
    return fetch(`/api/v1/history/${encodeURIComponent(info.hash)}/${info.index}`, {
      method: 'PUT', keepalive: true, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
    }).catch(() => {});
  }

  function togglePause() {
    if (!info || !msg.hidden) return;
    if (parkedAt >= 0) {
      start(parkedAt);
      return;
    }
    if (video.paused && mp && Date.now() - lastStat >= P.STALL_MS) {
      start(pos()); // на паузе буфер переполнился, загрузка встала (ревью 18Б, C1)
      return;
    }
    if (video.paused) video.play().catch(() => {});
    else video.pause();
  }

  // park — долгая пауза: кадр — заставкой, поток — остановить (Review Focus 4).
  function park() {
    if (!video.paused || !mp) return;
    try {
      const c = document.createElement('canvas');
      c.width = video.videoWidth;
      c.height = video.videoHeight;
      c.getContext('2d').drawImage(video, 0, 0);
      video.poster = c.toDataURL('image/jpeg', 0.8);
    } catch {
      // без заставки
    }
    const at = pos();
    stop();
    parkedAt = at;
  }

  function openMenu(kind) {
    if (!info) return;
    const subs = P.textSubs(info.subs);
    const labels = P.trackLabels(kind === 'audio' ? info.audio : subs);
    const items = kind === 'audio'
      ? info.audio.map((t, i) => ({ label: labels[i], on: t === audio, pick: () => setAudio(t) }))
      : [{ label: 'Выключены', on: !sub, pick: () => setSub(null) },
        ...subs.map((x, i) => ({ label: labels[i], on: x === sub, pick: () => setSub(x) }))];
    menu = kind;
    menuBox.replaceChildren(h('div', { class: 'pl-menu-h' }, kind === 'audio' ? 'Озвучка' : 'Субтитры'),
      ...items.map((it, i) => h('button', { class: it.on ? 'pl-item on' : 'pl-item', type: 'button', role: 'menuitemradio',
        'aria-checked': String(it.on), 'data-key': `pl-${kind}-${i}`, onclick: () => { closeMenu(); it.pick(); } },
      it.on ? icon('check', 18) : h('span', { class: 'pl-item-ic' }), it.label)));
    menuBox.hidden = false;
    (menuBox.querySelector('.on') || menuBox.querySelector('button')).focus({ preventScroll: true });
    showUI();
  }

  function closeMenu() {
    menu = null;
    menuBox.hidden = true;
  }

  function setAudio(t) {
    if (t === audio) return;
    audio = t;
    store.set(P.memKey(info.hash, 'audio'), JSON.stringify(P.memOf(t)));
    seekTo(pos());
  }

  function setSub(x) {
    sub = x;
    store.set(P.memKey(info.hash, 'subs'), JSON.stringify(P.memOf(x)));
    startSubs();
  }

  function onEnded() {
    if (!info || !mp) return;
    if (!P.nearEnd(pos(), dur())) { // поток кончился посреди файла — это обрыв, а не конец серии
      onError(null);
      return;
    }
    report(true); // досмотрели — просмотрено
    stop();
    if (!info.next) {
      leave();
      return;
    }
    let left = P.NEXT_AFTER;
    const text = h('div', { class: 'pl-msg-text' });
    const draw = () => { text.textContent = `Следующая: ${info.next.title} — через ${left} с`; };
    draw();
    nextBox.replaceChildren(text, h('div', { class: 'row gap10' },
      h('button', { class: 'btn inv', type: 'button', 'data-key': 'pl-next-go', onclick: goNext }, icon('skip_next'), 'Смотреть'),
      h('button', { class: 'btn', type: 'button', 'data-key': 'pl-next-cancel', onclick: cancelNext }, 'Отмена')));
    nextBox.hidden = false;
    nextBox.querySelector('button').focus({ preventScroll: true });
    nextTimer = setInterval(() => {
      left -= 1;
      if (left <= 0) goNext();
      else draw();
    }, 1000);
  }

  function cancelNext() {
    clearInterval(nextTimer);
    nextBox.hidden = true;
    leave();
  }

  function goPrev() {
    clearInterval(nextTimer);
    if (!info || !info.prev) return;
    report();
    location.replace(P.playHash(info.prev.src, false));
  }

  function goNext() {
    clearInterval(nextTimer);
    if (!info || !info.next) return;
    report();
    location.replace(P.playHash(info.next.src, false));
  }

  function leave() {
    if (ctx.prev) history.back();
    else location.replace(P.backHash(src));
  }

  function controls() { return [prevBtn, playBtn, back10Btn, fwd30Btn, nextBtn, audioBtn, subsBtn, muteBtn, fullBtn].filter(x => x && !x.hidden && !x.disabled); }

  function focusControl(delta) {
    const xs = controls();
    if (!xs.length) return;
    const i = Math.max(0, xs.indexOf(document.activeElement));
    xs[(i + delta + xs.length) % xs.length].focus({ preventScroll: true });
  }

  function showUI() {
    lastActivity = Date.now();
    wrap.classList.remove('idle');
  }

  function setVolume(v) {
    video.volume = v;
    video.muted = v === 0;
    saveVolume();
  }

  function toggleMute() {
    video.muted = !video.muted;
    saveVolume();
  }

  function saveVolume() {
    store.set('player.volume', String(video.volume));
    store.set('player.muted', video.muted ? '1' : '0');
    muteBtn.replaceChildren(icon(video.muted ? 'volume_off' : 'volume_up'));
  }

  function toggleFull() {
    if (document.fullscreenElement) document.exitFullscreen().catch(() => {});
    else if (wrap.requestFullscreen) wrap.requestFullscreen().catch(() => {});
  }

  // Клавиши — до nav.js (фаза перехвата): стрелки — перемотка и громкость, а не ходьба фокусом.
  function onKey(e) {
    if (e.altKey || e.ctrlKey || e.metaKey) return;
    showUI();
    const take = () => {
      e.preventDefault();
      e.stopPropagation();
    };
    if (!msg.hidden || !nextBox.hidden) {
      if (e.key === 'Escape') {
        take();
        if (!nextBox.hidden) cancelNext();
        else leave();
      }
      return; // стрелки и OK — по кнопкам окна
    }
    if (menu) {
      if (e.key === 'Escape') {
        take();
        closeMenu();
      }
      return; // стрелки по пунктам меню — nav.js
    }
    const key = e.key;
    const active = document.activeElement;
    if (key === 'ArrowUp') { backBtn.focus({ preventScroll: true }); take(); return; }
    if (key === 'ArrowDown') { if (active === bar) playBtn.focus({ preventScroll: true }); else if (active === backBtn) bar.focus({ preventScroll: true }); else focusControl(1); take(); return; }
    if ((key === 'ArrowLeft' || key === 'ArrowRight') && active && active.classList.contains('pl-btn')) { focusControl(key === 'ArrowLeft' ? -1 : 1); take(); return; }
    if (key === ' ' || key === 'k' || key === 'K' || key === 'л' || key === 'Л') togglePause();
    else if (key === 'ArrowLeft') jump(-10);
    else if (key === 'ArrowRight') jump(10);
    else if (key === 'm' || key === 'M' || key === 'ь' || key === 'Ь') toggleMute();
    else if (key === 'f' || key === 'F' || key === 'а' || key === 'А') toggleFull();
    else if (key === 'Escape') leave();
    else return;
    take();
  }

  const onFull = () => fullBtn.replaceChildren(icon(document.fullscreenElement ? 'fullscreen_exit' : 'fullscreen'));
  const onHide = () => report();

  video.addEventListener('playing', () => { load.hidden = true; });
  video.addEventListener('waiting', () => { load.hidden = false; });
  video.addEventListener('play', () => {
    clearTimeout(parkTimer);
    syncPlayBtn();
  });
  video.addEventListener('pause', () => {
    syncPlayBtn();
    if (!alive || !mp) return; // пауза от stop() при уходе или перезапуске — не пауза человека
    report();
    showUI();
    armPark();
  });
  video.addEventListener('timeupdate', () => {
    if (seek || !mp) return;
    if (video.currentTime > 0.5) played = true;
    showTime(pos());
    if (video.currentTime > 30) retried = false;
    if (!video.paused && P.reportDue(lastReport, Date.now())) report();
  });
  video.addEventListener('ended', onEnded);
  // Касание — ниже (pointerup); мышиные click и dblclick, которые Chrome шлёт вслед за касанием, — не мышь (ревью 18Б, I3).
  video.addEventListener('click', () => {
    if (!P.fromTouch(Date.now(), lastTouch)) togglePause();
  });
  video.addEventListener('dblclick', () => {
    if (!P.fromTouch(Date.now(), lastTouch)) toggleFull();
  });
  video.addEventListener('pointerup', (e) => {
    if (e.pointerType === 'mouse') return;
    const now = Date.now();
    lastTouch = now;
    const zone = P.tapZone(e.offsetX, video.clientWidth);
    clearTimeout(tapTimer);
    if (now - lastTap < 300 && zone) {
      lastTap = 0;
      jump(zone);
      return;
    }
    lastTap = now;
    tapTimer = setTimeout(() => {
      if (wrap.classList.contains('idle')) showUI();
      else wrap.classList.add('idle');
    }, 300);
  });
  wrap.addEventListener('mousemove', () => {
    if (!P.fromTouch(Date.now(), lastTouch)) showUI(); // mousemove вслед за касанием сразу показывал кнопки (ревью 18Б, I2)
  });
  bar.addEventListener('input', () => {
    dragging = true;
    time.textContent = `${P.fmtTime(Number(bar.value))} / ${P.fmtTime(dur())}`;
    showUI();
  });
  bar.addEventListener('change', () => {
    dragging = false;
    seekTo(Number(bar.value));
  });
  window.addEventListener('keydown', onKey, true);
  window.addEventListener('pagehide', onHide);
  document.addEventListener('fullscreenchange', onFull);
  const idle = setInterval(() => {
    const now = Date.now();
    if (P.hideDue(!video.paused, lastActivity, now, !!menu || dragging)) wrap.classList.add('idle');
    // Смотрим, а статистики mpegts.js давно нет — полный буфер MSE остановил загрузку насовсем: заново с того же места,
    // повтор не тратится (ревью 18Б, C1).
    if (mp && msg.hidden && nextBox.hidden && P.loaderStalled(lastStat, now, !video.paused)) start(pos());
  }, 1000);

  begin();

  return () => {
    report();
    alive = false;
    stop();
    clearInterval(idle);
    clearInterval(nextTimer);
    clearTimeout(seekTimer);
    clearTimeout(tapTimer);
    window.removeEventListener('keydown', onKey, true);
    window.removeEventListener('pagehide', onHide);
    document.removeEventListener('fullscreenchange', onFull);
    if (document.fullscreenElement) document.exitFullscreen().catch(() => {});
  };
}
