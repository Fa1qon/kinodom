// Просмотр источника канала прямо в пульте (план 14Д): видео через пересылку сервера — видно заглушку,
// качество, подвисания, задержку и совпадение с программой; «Кадры» — миниатюры всех источников канала.
// HLS — hls.js (или сам браузер), MPEG-TS — mpegts.js; обе библиотеки — копии в vendor/ (Apache-2.0).
import { h, icon, openModal, rating } from '../ui.js';

const libs = {};

// loadLib — библиотека плеера (сборка UMD кладёт объект в window) по первому требованию.
function loadLib(src, name) {
  if (globalThis[name]) return Promise.resolve(globalThis[name]);
  libs[src] ||= new Promise((resolve, reject) => {
    const s = document.createElement('script');
    s.src = src;
    s.onload = () => (globalThis[name] ? resolve(globalThis[name]) : reject(new Error('плеер не загрузился')));
    s.onerror = () => {
      delete libs[src];
      reject(new Error('плеер не загрузился'));
    };
    document.head.append(s);
  });
  return libs[src];
}

// playerKind — чем играть источник: hls, ts (поток MPEG-TS) или dash (в браузере не играем).
export function playerKind(src) {
  const u = (src.url || '').split('?')[0].toLowerCase();
  if (src.kind === 'dash' || u.endsWith('.mpd')) return 'dash';
  if (src.kind === 'hls' || u.endsWith('.m3u8')) return 'hls';
  return 'ts';
}

// watchURL — источник через пересылку сервера.
export function watchURL(id) {
  return `/api/v1/iptv/streams/${id}/watch`;
}

// previewStats — строка под видео: размер кадра, время до первого кадра, подвисания, задержка от эфира.
export function previewStats(s) {
  return [
    s.w > 0 && s.h > 0 ? `${s.w}×${s.h}` : null,
    s.firstMs > 0 ? `${rating(Math.round(s.firstMs / 100) / 10)} с до кадра` : null,
    `подвисаний ${s.stalls || 0}`,
    s.latency > 0 ? `задержка ${Math.round(s.latency)} с` : null,
  ].filter(Boolean).join(' · ');
}

// serverError — текст ошибки сервера из тела ответа ({"error": …}); тело не текст или не JSON — ''.
function serverError(details) {
  try {
    return JSON.parse(details.responseText).error || '';
  } catch {
    return '';
  }
}

const codec = { what: 'codec', text: 'Браузер не показывает этот поток' };
const net = (code, msg) => ({ what: 'net', text: msg ? `Источник не открылся: ${msg}` : code ? `Источник не открылся (ответ ${code})` : 'Источник не открылся' });

// playerError — остановка плеера словами (ревью 14Д, п. 4): {what: 'net' | 'codec', text}; не остановка —
// null. hls — (data) события Hls.Events.ERROR; mpegts — (тип, подробность, сведения) события ERROR.
export function playerError(lib, a, b, c) {
  if (lib === 'hls') {
    if (!a || !a.fatal) return null;
    if (a.type === 'mediaError' || a.type === 'muxError') return codec;
    if (a.type === 'networkError') return net(a.response && a.response.code, a.networkDetails ? serverError(a.networkDetails) : '');
    return { what: 'net', text: `Плеер остановился (${a.details || a.type})` };
  }
  if (a === 'MediaError') return codec;
  if (a === 'NetworkError') return net(c && c.code, '');
  return { what: 'net', text: `Плеер остановился (${b || a})` };
}

// nativeError — почему остановился свой HLS браузера (Chrome, WebView ТВ): он сам не говорит, поэтому список
// спрашивается ещё раз — status и body его ответа (0 — запрос не дошёл); code и message — video.error.
export function nativeError(code, message, status, body) {
  if (status !== 200) return net(status, serverError({ responseText: body }));
  if (code === 3 || /DECODE|NO_SUPPORTED_STREAMS|CODEC/i.test(message || '')) return codec;
  return net(0, '');
}

// whyNative — nativeError для источника url после ошибки video.
async function whyNative(video, url) {
  const e = video.error || {};
  try {
    const r = await fetch(url, { cache: 'no-store' });
    return nativeError(e.code, e.message, r.status, await r.text());
  } catch {
    return nativeError(e.code, e.message, 0, '');
  }
}

// frameNote — подпись вместо кадра: data URL — кадр есть (null); иначе — почему его нет.
export function frameNote(shot) {
  if (String(shot).startsWith('data:')) return null;
  return { codec: 'браузер не показывает этот поток', dash: 'DASH в браузере не показывается', error: 'источник не открылся' }[shot] || 'нет картинки';
}

// playerSession — плеер окна: ready — подключённый плеер; close() разбирает его сейчас или, если окно закрыли,
// пока грузилась библиотека, — как только подключится (ready тогда — null; ревью 14Д, п. 3).
export function playerSession(pending) {
  let player = null;
  let closed = false;
  const ready = pending.then((p) => {
    if (closed) {
      p.destroy();
      return null;
    }
    player = p;
    return p;
  });
  return {
    ready,
    close() {
      closed = true;
      if (player) player.destroy();
      player = null;
    },
  };
}

// hlsWay — чем играть HLS: hls.js — везде, где есть MSE (свой HLS Chrome 154 не разбирает часть потоков, которые
// hls.js играет; вживую 14Д); свой HLS браузера — только без MSE (iPhone); null — нечем.
export function hlsWay(mse, native) {
  if (mse) return 'hls.js';
  return native ? 'native' : null;
}

// attach — источник в <video>: {destroy, latency()}; остановка плеера — onFail({what, text}). Библиотека не
// загрузилась или браузер её не тянет — исключение.
async function attach(video, url, kind, onFail = () => {}) {
  if (kind === 'hls') {
    const Hls = await loadLib('vendor/hls.light.min.js', 'Hls').catch(() => null);
    const way = hlsWay(!!Hls && Hls.isSupported(), !!video.canPlayType('application/vnd.apple.mpegurl'));
    if (!way) throw new Error('браузер не умеет HLS');
    if (way === 'native') {
      video.addEventListener('error', () => whyNative(video, url).then(onFail), { once: true });
      video.src = url;
      return { destroy: () => { video.removeAttribute('src'); video.load(); }, latency: () => 0 };
    }
    video.addEventListener('error', () => onFail(codec), { once: true });
    const hls = new Hls({ enableWorker: true, maxBufferLength: 10 });
    hls.on(Hls.Events.ERROR, (_, data) => {
      const e = playerError('hls', data);
      if (e) onFail(e);
    });
    hls.loadSource(url);
    hls.attachMedia(video);
    return { destroy: () => hls.destroy(), latency: () => hls.latency || 0 };
  }
  const mpegts = await loadLib('vendor/mpegts.js', 'mpegts');
  if (!mpegts.isSupported()) throw new Error('браузер не умеет MPEG-TS');
  video.addEventListener('error', () => onFail(codec), { once: true });
  const p = mpegts.createPlayer({ type: 'mpegts', isLive: true, url }, { enableWorker: false, liveBufferLatencyChasing: true });
  p.on(mpegts.Events.ERROR, (type, detail, info) => onFail(playerError('mpegts', type, detail, info)));
  p.attachMediaElement(video);
  p.load();
  return {
    destroy: () => {
      try {
        p.pause();
        p.unload();
        p.detachMediaElement();
      } finally {
        p.destroy();
      }
    },
    latency: () => (video.buffered.length ? video.buffered.end(video.buffered.length - 1) - video.currentTime : 0),
  };
}

// openPreview — окно «Смотреть»: видео источника src, статистика раз в секунду; now — текущая передача канала
// («» — нет). Закрытие обрывает пересылку.
export function openPreview(src, title, now = '') {
  const kind = playerKind(src);
  const video = h('video', { class: 'preview-video', muted: true, autoplay: true, playsinline: true, controls: true });
  video.muted = true;
  const stats = h('div', { class: 'muted small', role: 'status' });
  const err = h('div', { class: 'error' });
  let session = null;
  let timer = 0;
  let modal = null;
  const close = () => {
    clearInterval(timer);
    if (session) session.close();
    modal.close();
  };
  const closeBtn = h('button', { class: 'btn', type: 'button', 'data-key': 'preview-close', onclick: close }, icon('close'), 'Закрыть');
  const box = h('div', { class: 'dlg card preview', role: 'dialog', 'aria-modal': 'true', 'aria-label': title },
    h('div', { class: 'h' }, title), now ? h('div', { class: 'muted small' }, now) : null, video, stats, err, h('div', { class: 'row gap10' }, closeBtn));
  modal = openModal(box, close);
  closeBtn.focus({ preventScroll: true });
  if (kind === 'dash') {
    err.textContent = 'DASH в пульте не показывается — откройте канал в VLC';
    return close;
  }
  const s = { w: 0, h: 0, firstMs: 0, stalls: 0, latency: 0 };
  const started = performance.now();
  video.addEventListener('playing', () => {
    if (!s.firstMs) s.firstMs = performance.now() - started;
  });
  video.addEventListener('waiting', () => {
    if (s.firstMs) s.stalls++;
  });
  session = playerSession(attach(video, watchURL(src.id), kind, (e) => {
    err.textContent = e.text;
  }));
  session.ready.then((player) => {
    if (!player) return;
    timer = setInterval(() => {
      s.w = video.videoWidth;
      s.h = video.videoHeight;
      s.latency = player.latency();
      stats.textContent = previewStats(s);
    }, 1000);
  }, (e) => {
    err.textContent = e.message;
  });
  return close;
}

// grabFrame — кадр источника для «Кадров»: открыть без звука, дождаться картинки, снять 320×180 — data URL.
// Кадра нет — почему (frameNote): 'dash', 'codec' (браузер не показывает), 'error' (источник не открылся) —
// сразу; 'none' — нет картинки за timeoutMs.
export async function grabFrame(src, timeoutMs = 8000) {
  const kind = playerKind(src);
  if (kind === 'dash') return 'dash';
  // Без autoplay и в пределах окна (невидимым): Chrome ставит на паузу беззвучный автозапуск вне экрана —
  // кадр не приходил (вживую 14Д); явный play() так не останавливается.
  const video = h('video', { muted: true, playsinline: true, class: 'frame-probe' });
  video.muted = true;
  document.body.append(video);
  let player = null;
  let failed = '';
  let stop = null;
  try {
    player = await attach(video, watchURL(src.id), kind, (e) => {
      failed = e.what === 'codec' ? 'codec' : 'error';
      if (stop) stop();
    });
    video.play().catch(() => {});
    // Кадр — после полутора секунд эфира: первые кадры бывают чёрными (затемнение, ключевой кадр не пришёл).
    const ok = await new Promise((resolve) => {
      if (failed) return resolve(false);
      const t = setTimeout(() => resolve(video.currentTime > 0 && video.videoWidth > 0), timeoutMs);
      stop = () => {
        clearTimeout(t);
        resolve(false);
      };
      const tick = () => {
        if (video.currentTime >= 1.5 && video.videoWidth > 0) {
          clearTimeout(t);
          video.removeEventListener('timeupdate', tick);
          resolve(true);
        }
      };
      video.addEventListener('timeupdate', tick);
    });
    if (!ok) return failed || 'none';
    const c = document.createElement('canvas');
    c.width = 320;
    c.height = 180;
    c.getContext('2d').drawImage(video, 0, 0, 320, 180);
    return c.toDataURL('image/jpeg', 0.7);
  } catch {
    return 'error';
  } finally {
    if (player) player.destroy();
    video.remove();
  }
}
