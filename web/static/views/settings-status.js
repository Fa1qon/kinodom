// «Настройки → Состояние»: потоки и скорости, модули, трекеры, место, проблемы (спека этапа 7,
// разделы 5.7 и 6.3).
import { h, fill, size, speed, ago, poll, icon } from '../ui.js';
import { get } from '../api.js';
import { layout } from './settings-layout.js';
import { appBridge } from './tvkit.js';

// MODULES — шесть модулей на экране; нет модуля у сервера — «ещё не сделан». DLNA исключён (решение заказчика
// 2026-09-30) — на его месте обнаружение сервера приложением (спека этапа 13, раздел 5.1).
const MODULES = [['torrents', 'Торренты'], ['catalog', 'Каталог'], ['ratings', 'Кинопоиск'], ['library', 'Медиатека'], ['iptv', 'IPTV'], ['discovery', 'Обнаружение']];
const MODULE_STATE = {
  running: ['работает', 'var(--green)'],
  starting: ['запускается', 'var(--yellow)'],
  restarting: ['перезапускается', 'var(--yellow)'],
  stopped: ['остановлен', 'var(--yellow)'],
  disabled: ['выключен', 'var(--faint)'],
};
// hhmm — «18:05» по часам устройства.
const hhmm = (t) => {
  const d = new Date(t);
  return String(d.getHours()).padStart(2, '0') + ':' + String(d.getMinutes()).padStart(2, '0');
};

// kpLine — строка Кинопоиска (спека 11b, 5.7): без токена — работает или пауза до ЧЧ:ММ; ключ — не задан
// (не беда: он запасной), N из M за сутки, не подошёл, квота до ЧЧ:ММ. warn — жёлтым.
export function kpLine(k) {
  const kl = k.keyless || {};
  const web = kl.pausedUntil ? `без токена: пауза до ${hhmm(kl.pausedUntil)}` + (kl.reason ? ` (${kl.reason})` : '') : 'без токена: работает';
  const key = !k.keySet ? 'ключ не задан'
    : k.badKey ? 'ключ не подошёл'
      : k.quotaUntil ? `ключ: квота до ${hhmm(k.quotaUntil)}`
        : `ключ: ${k.dailyUsed} из ${k.dailyLimit} за сутки`;
  return { text: `${web} · ${key}`, warn: !!kl.pausedUntil || (k.keySet && k.badKey) };
}

// appLine — адрес, по которому ТВ и телефон скачают приложение (спека этапа 13, 5.3): первый адрес ПК в домашней
// сети; APK на сервере нет или адреса нет — null.
export function appLine(app, addrs) {
  return app && addrs && addrs.length ? addrs[0] + '/app' : null;
}

// appModel — карточка «Приложение для ТВ и телефона» (правки после 13b, заказчик 2026-10-01): APK на сервере нет —
// null; в браузере — «Скачать» и адрес для ТВ; в приложении — версии и действие: update — приложение обновится по
// кнопке, download — у приложения 13a/13b нет update(), latest — та же версия или новее серверной.
export function appModel(app, addrs, bridge) {
  if (!app) return null;
  if (!bridge) return { mode: 'browser', version: app.version, download: app.url, address: appLine(app, addrs) };
  const installed = typeof bridge.version === 'function' ? bridge.version() : '';
  const code = typeof bridge.versionCode === 'function' ? bridge.versionCode() : 0;
  const newer = code ? app.versionCode > code : app.version !== installed;
  const action = !newer ? 'latest' : typeof bridge.update === 'function' ? 'update' : 'download';
  return { mode: 'app', installed, server: app.version, action, download: app.url };
}

// appCard — карточка по appModel; bridge — для «Обновить».
export function appCard(m, bridge) {
  if (!m) return null;
  const title = h('div', { class: 'h' }, 'Приложение для ТВ и телефона');
  const download = () => h('a', { class: 'btn inv', href: m.download, download: '', 'data-key': 'app-download' }, icon('download'), 'Скачать');
  if (m.mode === 'browser') {
    return h('div', { class: 'card tight app-card' }, title,
      h('div', { class: 'row wrap' }, download(), h('span', { class: 'muted' }, m.version)),
      m.address ? h('div', { class: 'app-addr' }, m.address) : null);
  }
  const action = m.action === 'update'
    ? h('button', { class: 'btn inv', type: 'button', 'data-key': 'app-update', onclick: () => bridge.update() }, icon('download'), 'Обновить')
    : m.action === 'download' ? download() : h('span', { class: 'muted' }, 'Последняя версия');
  return h('div', { class: 'card tight app-card' }, title,
    h('div', { class: 'muted' }, `Установлено ${m.installed}`),
    h('div', { class: 'muted' }, `На сервере ${m.server}`),
    h('div', { class: 'row wrap' }, action));
}

const LOGIN = {
  none: 'логин не задан',
  unknown: 'вход ещё не проверялся',
  ok: 'вход выполнен',
};

export function render(root, r, ctx) {
  const content = layout(root, 'status', 'Состояние');
  let alive = true;
  let appInfo = null; // «Приложение для ТВ и телефона»: сведения об APK и адреса — один раз
  Promise.all([get('/app').catch(() => null), get('/setup/addresses').catch(() => [])]).then(([app, addrs]) => {
    appInfo = appModel(app, addrs, appBridge());
  });
  // Раз в 2 с — скорости меняются быстро; отметка проблем в меню обновляется своим опросом.
  const refresh = poll(async () => {
    let st;
    try {
      st = await get('/status');
    } catch (e) {
      content.replaceChildren(h('p', { class: 'error' }, e.message));
      return;
    }
    if (alive) draw(st);
  }, 2000);

  function draw(st) {
    // fill, а не replaceChildren: карточки приложения ещё нет (сведения грузятся) — пусто, а не «null».
    fill(content,
      h('div', { class: 'tiles' },
        tile('Потоки', String(st.streams.count)),
        tile('Загрузка', speed(st.streams.downloadSpeed)),
        tile('Раздача', speed(st.streams.uploadSpeed))),
      h('div', { class: 'two-cols' },
        h('div', { class: 'card tight' }, h('div', { class: 'h' }, 'Модули'), MODULES.map(([id, name]) => moduleRow(st, id, name))),
        h('div', { class: 'col' },
          h('div', { class: 'card tight' }, h('div', { class: 'h' }, 'Трекеры'),
            trackerRow('Rutracker', st.trackers.rutracker), trackerRow('Rutor', st.trackers.rutor)),
          disk(st))),
      h('div', { class: 'card tight' }, h('div', { class: 'h' }, 'Проблемы'),
        st.problems.length
          ? st.problems.map((p) => h('div', { class: 'mod' }, h('span', { class: 'mark', style: { background: 'var(--yellow)' } }), h('span', { class: 'grow' }, p.text), h('span', { class: 'muted small mod-when' }, ago(p.since))))
          : h('div', { class: 'muted' }, 'Проблем нет')),
      appCard(appInfo, appBridge()),
    );
  }

  function moduleRow(st, id, name) {
    const m = st.modules.find((x) => x.name === id);
    if (!m) return row('var(--faint)', name, 'ещё не сделан', 'var(--faint)');
    const [text, color] = MODULE_STATE[m.state] || [m.state, 'var(--yellow)'];
    let detail = text;
    if (id === 'ratings' && m.state === 'running') {
      const { text: line, warn } = kpLine(st.kinopoisk);
      if (warn) return row('var(--yellow)', name, line, 'var(--yellow)');
      detail = line;
    }
    if (m.lastError && m.state !== 'running') detail += ' · ' + m.lastError;
    return row(color, name, detail, color === 'var(--green)' ? 'var(--muted)' : color);
  }

  function trackerRow(name, t) {
    if (!t) return row('var(--faint)', name, 'нет данных', 'var(--muted)');
    const ok = t.state === 'ok';
    const lines = [ok ? `Работает · каталог обновлён ${ago(t.updatedAt)}` : t.text || 'не отвечает'];
    if (t.login) {
      const extra = loginExtra(ok ? '' : t.text, t.login.text || LOGIN[t.login.state] || t.login.state);
      if (extra) lines.push(extra);
    }
    return row(ok ? 'var(--green)' : 'var(--yellow)', name, lines.join(' · '), ok ? 'var(--muted)' : 'var(--yellow)');
  }

  function disk(st) {
    const d = st.disk;
    const low = d.low || st.problems.some((p) => p.id === 'torrents.space');
    if (!d.totalBytes) return h('div', { class: 'card' }, h('div', { class: 'h' }, 'Папка загрузок'), h('div', { class: 'muted' }, 'нет данных'));
    const pct = (n) => `${Math.max(0, Math.min(100, (n / d.totalBytes) * 100))}%`;
    const other = d.totalBytes - d.freeBytes - d.usedBytes;
    return h('div', { class: 'card' },
      h('div', { class: 'row' }, h('span', { class: 'h grow' }, 'Папка загрузок'), low ? h('span', { class: 'small', style: { color: 'var(--yellow)' } }, 'Мало места') : null),
      h('div', { class: 'diskbar' }, h('div', { style: { width: pct(other), background: 'var(--buffer)' } }), h('div', { style: { width: pct(d.usedBytes), background: 'var(--text)' } })),
      h('div', { class: 'legend small' },
        h('span', null, h('span', { class: 'mark', style: { background: 'var(--text)' } }), `Kinodom ${size(d.usedBytes)}`),
        h('span', null, h('span', { class: 'mark', style: { background: 'var(--buffer)' } }), 'Другое'),
        h('span', { class: 'muted' }, `Свободно ${size(d.freeBytes)}`)));
  }

  return () => {
    alive = false;
    refresh.stop();
  };
}

function tile(label, value) {
  return h('div', { class: 'tile' }, h('div', { class: 'muted small' }, label), h('div', { class: 'v' }, value));
}

// row — строка модуля или трекера; длинное пояснение (long) на телефоне — под именем, во всю ширину.
function row(mark, name, text, color) {
  return h('div', { class: text.length > 24 ? 'mod long' : 'mod' },
    h('span', { class: 'mark', style: { background: mark } }),
    h('span', { class: 'mod-name', style: { color: mark === 'var(--faint)' ? 'var(--faint)' : null } }, name),
    h('span', { class: 'grow mod-text', style: { color } }, text));
}

// loginExtra — строка входа Rutracker, если она не повторяет строку трекера (хвост Х23: «Rutracker:
// неверный логин или пароль · Неверный логин или пароль»).
export function loginExtra(trackerText, loginText) {
  const norm = (x) => (x || '').toLowerCase().replace(/^rutracker:\s*/, '').trim();
  return loginText && norm(trackerText) !== norm(loginText) ? loginText : '';
}
