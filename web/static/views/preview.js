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

// attach — источник в <video>: {destroy, latency()}. Ошибка библиотеки — исключение.
async function attach(video, url, kind) {
  if (kind === 'hls') {
    if (video.canPlayType('application/vnd.apple.mpegurl')) {
      video.src = url;
      return { destroy: () => { video.removeAttribute('src'); video.load(); }, latency: () => 0 };
    }
    const Hls = await loadLib('vendor/hls.light.min.js', 'Hls');
    if (!Hls.isSupported()) throw new Error('браузер не умеет HLS');
    const hls = new Hls({ enableWorker: true, maxBufferLength: 10 });
    hls.loadSource(url);
    hls.attachMedia(video);
    return { destroy: () => hls.destroy(), latency: () => hls.latency || 0 };
  }
  const mpegts = await loadLib('vendor/mpegts.js', 'mpegts');
  if (!mpegts.isSupported()) throw new Error('браузер не умеет MPEG-TS');
  const p = mpegts.createPlayer({ type: 'mpegts', isLive: true, url }, { enableWorker: false, liveBufferLatencyChasing: true });
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
  let player = null;
  let timer = 0;
  let modal = null;
  const close = () => {
    clearInterval(timer);
    if (player) player.destroy();
    player = null;
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
  attach(video, watchURL(src.id), kind).then((p) => {
    if (!modal) return p.destroy();
    player = p;
    timer = setInterval(() => {
      s.w = video.videoWidth;
      s.h = video.videoHeight;
      s.latency = player ? player.latency() : 0;
      stats.textContent = previewStats(s);
    }, 1000);
  }, (e) => {
    err.textContent = e.message;
  });
  video.addEventListener('error', () => {
    err.textContent = 'Источник не открылся';
  });
  return close;
}

// grabFrame — кадр источника для «Кадров»: открыть без звука, дождаться картинки, снять 320×180; нет картинки за
// timeoutMs — null.
export async function grabFrame(src, timeoutMs = 8000) {
  const kind = playerKind(src);
  if (kind === 'dash') return null;
  const video = h('video', { muted: true, autoplay: true, playsinline: true, class: 'frame-probe' });
  video.muted = true;
  document.body.append(video);
  let player = null;
  try {
    player = await attach(video, watchURL(src.id), kind);
    const ok = await new Promise((resolve) => {
      const t = setTimeout(() => resolve(false), timeoutMs);
      video.addEventListener('playing', () => setTimeout(() => {
        clearTimeout(t);
        resolve(video.videoWidth > 0);
      }, 300), { once: true });
      video.addEventListener('error', () => {
        clearTimeout(t);
        resolve(false);
      }, { once: true });
    });
    if (!ok) return null;
    const c = document.createElement('canvas');
    c.width = 320;
    c.height = 180;
    c.getContext('2d').drawImage(video, 0, 0, 320, 180);
    return c.toDataURL('image/jpeg', 0.7);
  } catch {
    return null;
  } finally {
    if (player) player.destroy();
    video.remove();
  }
}
