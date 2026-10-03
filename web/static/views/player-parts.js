// Чистые части плеера фильмов (спека цикла 18, раздел 5; план 18Б): подписи и выбор дорожек, время, перемотка
// стрелками, адреса потока, разбор WebVTT по мере прихода, ссылка внешнего плеера. Без DOM — проверяются в Node.

// LANGS — язык дорожки (ISO 639-2) по-русски.
export const LANGS = {
  rus: 'Русский', eng: 'Английский', ukr: 'Украинский', jpn: 'Японский', ger: 'Немецкий', deu: 'Немецкий', fre: 'Французский',
  fra: 'Французский', spa: 'Испанский', ita: 'Итальянский', kor: 'Корейский', chi: 'Китайский', zho: 'Китайский',
};

// trackLabel — «Русский — Дубляж»; без названия — язык; без языка — название; без того и другого — «Дорожка N».
export function trackLabel(t, i) {
  const lang = LANGS[t.lang] || (t.lang || '').toUpperCase();
  const title = (t.title || '').trim();
  if (lang && title && title.toLowerCase() !== lang.toLowerCase()) return `${lang} — ${title}`;
  return lang || title || `Дорожка ${i + 1}`;
}

// CODECS — кодек дорожки для подписи.
const CODECS = { ac3: 'AC3', eac3: 'E-AC3', dts: 'DTS', truehd: 'TrueHD', aac: 'AAC', mp3: 'MP3', flac: 'FLAC', opus: 'Opus', vorbis: 'Vorbis', alac: 'ALAC' };
const CHANNELS = { 1: '1.0', 2: '2.0', 6: '5.1', 8: '7.1' };

// trackLabels — подписи всех дорожек: одинаковые («Русский» и «Русский» у «Полдня», живая проверка 18Б) — с кодеком и
// каналами («Русский · AC3 5.1»), и всё ещё одинаковые — с номером.
export function trackLabels(tracks) {
  const dup = (ls) => ls.map((l) => ls.filter((x) => x === l).length > 1);
  const base = tracks.map((t, i) => trackLabel(t, i));
  const d1 = dup(base);
  const withCodec = base.map((l, i) => {
    if (!d1[i]) return l;
    const t = tracks[i];
    const c = [CODECS[t.codec] || (t.codec || '').toUpperCase(), CHANNELS[t.channels] || ''].filter(Boolean).join(' ');
    return c ? `${l} · ${c}` : l;
  });
  const d2 = dup(withCodec);
  return withCodec.map((l, i) => (d2[i] ? `${l} · ${i + 1}` : l));
}

// pickTrack — озвучка по памяти раздачи: то же название, затем тот же язык и кодек, затем язык; иначе главная, иначе первая.
export function pickTrack(tracks, mem) {
  if (!tracks || !tracks.length) return null;
  const byTitle = mem && mem.title ? tracks.find((x) => x.title === mem.title) : null;
  const byCodec = mem && mem.lang && mem.codec ? tracks.find((x) => x.lang === mem.lang && x.codec === mem.codec) : null;
  const byLang = mem && mem.lang ? tracks.find((x) => x.lang === mem.lang) : null;
  return byTitle || byCodec || byLang || tracks.find((x) => x.default) || tracks[0];
}

// textSubs — субтитры, которые браузер покажет (не картинки).
export function textSubs(subs) {
  return (subs || []).filter((x) => !x.image);
}

// pickSub — субтитры по памяти: «выключены» помнятся; иначе — то же название, затем язык; без памяти — выключены.
export function pickSub(subs, mem) {
  if (!mem || mem.off) return null;
  const text = textSubs(subs);
  return (mem.title && text.find((x) => x.title === mem.title)) || (mem.lang && text.find((x) => x.lang === mem.lang)) || null;
}

// memOf — что запомнить о выборе (null — субтитры выключены); кодек — чтобы различить дорожки одного языка без названий.
export function memOf(t) {
  return t ? { title: t.title || '', lang: t.lang || '', codec: t.codec || '' } : { off: true };
}

// memKey — ключ памяти выбора в браузере: по раздаче, отдельно озвучка и субтитры.
export function memKey(hash, kind) {
  return `player.${kind}.${hash}`;
}

export function readMem(raw) {
  try {
    const v = JSON.parse(raw);
    return v && typeof v === 'object' ? v : null;
  } catch {
    return null;
  }
}

// fmtTime — «23:14», «1:02:03».
export function fmtTime(sec) {
  const s = Math.max(0, Math.floor(sec || 0));
  const p = (n) => String(n).padStart(2, '0');
  const hh = Math.floor(s / 3600);
  const mm = Math.floor(s / 60) % 60;
  return hh > 0 ? `${hh}:${p(mm)}:${p(s % 60)}` : `${mm}:${p(s % 60)}`;
}

// SEEK_GAP — нажатия ←/→ с перерывом меньше этого складываются в одну перемотку: поток перезапускается один раз.
export const SEEK_GAP = 600;

// seekStep — перемотка стрелкой на delta: подряд — от накопленной цели, после перерыва — от текущего места base.
export function seekStep(state, delta, now, base) {
  const from = state && now - state.at < SEEK_GAP ? state.target : base;
  return { target: from + delta, at: now };
}

// clampPos — место в пределах файла (не дальше чем за секунду до конца).
export function clampPos(t, dur) {
  return Math.max(0, Math.min(t, dur > 1 ? dur - 1 : t));
}

// positionOf — место в файле: ключевой кадр начала потока плюс время от начала потока.
export function positionOf(k, currentTime) {
  return Math.round((k + Math.max(currentTime || 0, 0)) * 1000) / 1000;
}

const t3 = (x) => Math.round(x * 1000) / 1000;

// streamURL — поток сервера (план 18А) с кадра k: MPEG-TS, озвучка audio (null — в файле нет звука).
export function streamURL(src, k, audio, sid) {
  return `/play/${src}/stream.ts?t=${t3(k)}&sid=${sid}` + (audio ? `&a=${audio.id}` : '');
}

// subsURL — субтитры сервера с кадра k (время реплик — время файла).
export function subsURL(src, sub, k, sid) {
  return `/play/${src}/subs/${encodeURIComponent(sub.id)}.vtt?t=${t3(k)}&sid=${sid}`;
}

// newSid — номер плеера для сервера: перемотка того же плеера не занимает новое место.
export function newSid(rand = Math.random) {
  const abc = 'abcdefghijklmnopqrstuvwxyz0123456789';
  return Array.from({ length: 16 }, () => abc[Math.floor(rand() * abc.length)]).join('');
}

// canShow — браузер покажет видео: строка кодека есть, и MSE её принимает (mpegts.js кладёт поток в MSE как MP4).
export function canShow(mime, isTypeSupported) {
  return !!mime && typeof isTypeSupported === 'function' && isTypeSupported(`video/mp4; codecs="${mime}"`);
}

// REPORT_EVERY — как часто сообщать место во время просмотра (спека 18, раздел 4).
export const REPORT_EVERY = 10000;

export function reportDue(last, now) {
  return now - last >= REPORT_EVERY;
}

// reportBody — тело PUT /history: место в пределах файла, до десятых.
export function reportBody(pos, dur) {
  return { positionSec: Math.round(Math.min(Math.max(pos, 0), dur) * 10) / 10, durationSec: dur };
}

// vttTime — «ММ:СС.ммм» или «ЧЧ:ММ:СС.ммм» в секундах; не время — null.
export function vttTime(s) {
  const parts = String(s).trim().split(':');
  if (parts.length < 2 || parts.length > 3) return null;
  let t = 0;
  for (const p of parts) {
    const v = Number(p);
    if (!Number.isFinite(v) || v < 0 || p === '') return null;
    t = t * 60 + v;
  }
  return Math.round(t * 1000) / 1000;
}

function parseCue(block) {
  const lines = block.split('\n');
  const i = lines.findIndex((l) => l.includes(' --> '));
  if (i < 0 || i + 1 >= lines.length) return null;
  const [from, rest] = lines[i].split(' --> ');
  const start = vttTime(from);
  const end = vttTime(rest.trim().split(/\s+/)[0]);
  if (start === null || end === null) return null;
  return { start, end, text: lines.slice(i + 1).join('\n') };
}

// cueParser — разбор WebVTT по мере прихода: push(кусок) → готовые реплики; final — конец потока.
export function cueParser() {
  let buf = '';
  return {
    push(chunk, final) {
      buf += String(chunk || '').replace(/\r\n/g, '\n');
      const out = [];
      for (;;) {
        const i = buf.indexOf('\n\n');
        if (i < 0) break;
        const c = parseCue(buf.slice(0, i));
        buf = buf.slice(i + 2);
        if (c) out.push(c);
      }
      if (final && buf.trim()) {
        const c = parseCue(buf.trim());
        buf = '';
        if (c) out.push(c);
      }
      return out;
    },
  };
}

// externalLink — «Открыть во внешнем плеере» на этом устройстве: Android — VLC ссылкой intent (исходный файл с
// места, без own=1: место по чтению угадывает сервер), иначе — .m3u8.
export function externalLink(info, ua) {
  if (!/Android/i.test(ua)) return info.m3uUrl;
  const u = new URL(info.direct);
  u.searchParams.delete('own');
  const tail = `S.title=${encodeURIComponent(info.title)};S.browser_fallback_url=${encodeURIComponent(info.m3uUrl)};end`;
  return `intent://${u.host}${u.pathname}${u.search}#Intent;scheme=${u.protocol.replace(':', '')};type=video/*;package=org.videolan.vlc;`
    + (info.startSec > 0 ? `l.position=${info.startSec * 1000};` : '') + tail;
}

// webPlayer — «Смотреть» открывает плеер в браузере: не в приложении (bridge), у сервера есть ffmpeg, на этом
// устройстве не выбран внешний плеер (choice — «Параметры» → «На этом устройстве»).
export function webPlayer(status, choice, bridge) {
  return !bridge && !!(status && status.transcoder) && choice !== 'external';
}

// playHash — адрес страницы плеера.
export function playHash(src, fromStart) {
  return `#/play/${src}` + (fromStart ? '?fromStart=1' : '');
}

// srcOf — src из частей адреса после «play»: ['torrent', hash, номер] или ['library', номер файла]; иначе null.
export function srcOf(parts) {
  if (parts.length === 3 && parts[0] === 'torrent' && /^[0-9a-f]{40}$/i.test(parts[1]) && /^\d+$/.test(parts[2])) {
    return `torrent/${parts[1].toLowerCase()}/${parts[2]}`;
  }
  if (parts.length === 2 && parts[0] === 'library' && /^\d+$/.test(parts[1])) return `library/${parts[1]}`;
  return null;
}

// backHash — куда «Назад», если плеер открыли по прямой ссылке.
export function backHash(src) {
  return src && src.startsWith('library/') ? '#/library' : '#/downloads';
}

// tapZone — двойное касание: левая треть — назад на 10 с, правая — вперёд, середина — ничего.
export function tapZone(x, width) {
  if (x < width / 3) return -10;
  if (x > (width * 2) / 3) return 10;
  return 0;
}

// HIDE_AFTER — кнопки прячутся через столько без движения, пока идёт просмотр (спека 18, 5.2).
export const HIDE_AFTER = 3000;

// hideDue — пора прятать кнопки: идёт просмотр, давно не трогали, не открыто меню и не тянут шкалу (held).
export function hideDue(playing, lastActivity, now, held) {
  return playing && !held && now - lastActivity >= HIDE_AFTER;
}

export function volumeStep(v, d) {
  return Math.round(Math.max(0, Math.min(1, v + d)) * 10) / 10;
}

// nearEnd — поток кончился у конца файла (последние 30 с), а не оборвался: только тогда «просмотрено» и следующая серия.
export function nearEnd(pos, dur) {
  return dur > 0 && pos >= dur - 30;
}

// STALL_MS — mpegts.js шлёт статистику каждые 0,6 с, пока загрузка жива; полный буфер MSE останавливает загрузку насовсем
// и статистику тоже (ревью 18Б, C1). Медленная раздача шлёт статистику и с нулевой скоростью — её не трогаем.
export const STALL_MS = 4000;

// loaderStalled — смотрим, а статистики давно нет: загрузка встала, поток — заново с того же места.
export function loaderStalled(lastStat, now, playing) {
  return playing && now - lastStat >= STALL_MS;
}

// playButton — вид кнопки по состоянию видео: отказ автозапуска не даёт события pause (ревью 18Б, I1).
export function playButton(paused) {
  return paused ? { icon: 'play_arrow', label: 'Смотреть' } : { icon: 'pause', label: 'Пауза' };
}

// TOUCH_MOUSE_MS — Chrome шлёт мышиные события (mousemove, click, dblclick) вслед за касанием; в это окно они — не мышь
// (ревью 18Б, I2, I3).
export const TOUCH_MOUSE_MS = 800;

export function fromTouch(now, lastTouch) {
  return now - lastTouch < TOUCH_MOUSE_MS;
}

// positionNow — место в файле сейчас: на долгой паузе — где остановились; пока поток с нового места запускается — куда
// идём (ревью 18Б, I5); иначе — кадр начала потока плюс время потока.
export function positionNow({ parkedAt, pendingAt, k, currentTime }) {
  if (parkedAt >= 0) return parkedAt;
  if (pendingAt >= 0) return pendingAt;
  return positionOf(k, currentTime);
}

// shouldReport — сообщать ли место: только когда что-то посмотрели (иначе «Продолжить» и сразу «Назад» сдвигали место
// назад, ревью 18Б, Minor 8) или досмотрели до конца.
export function shouldReport(played, final) {
  return played || final;
}

// NEXT_AFTER — отсчёт до следующей серии, с (спека 18, 5.5).
export const NEXT_AFTER = 10;

// PARK_AFTER — пауза дольше этого — поток останавливается (сервер отдаёт в темпе просмотра и на паузе, браузер
// копил бы), продолжение — с того же места (план 18Б, Review Focus 4).
export const PARK_AFTER = 60000;
