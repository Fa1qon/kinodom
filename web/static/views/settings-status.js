// «Настройки → Состояние»: потоки и скорости, модули, трекеры, место, проблемы (спека этапа 7,
// разделы 5.7 и 6.3).
import { h, size, speed, ago, poll } from '../ui.js';
import { get } from '../api.js';
import { layout } from './settings-layout.js';

// MODULES — шесть модулей на экране; нет модуля у сервера — «ещё не сделан» (этапы 8–10).
const MODULES = [['torrents', 'Торренты'], ['catalog', 'Каталог'], ['ratings', 'Кинопоиск'], ['library', 'Медиатека'], ['iptv', 'IPTV'], ['dlna', 'DLNA']];
const MODULE_STATE = {
  running: ['работает', 'var(--green)'],
  starting: ['запускается', 'var(--yellow)'],
  restarting: ['перезапускается', 'var(--yellow)'],
  stopped: ['остановлен', 'var(--yellow)'],
  disabled: ['выключен', 'var(--faint)'],
};
const LOGIN = {
  none: 'логин не задан',
  unknown: 'вход ещё не проверялся',
  ok: 'вход выполнен',
};

export function render(root, r, ctx) {
  const content = layout(root, 'status', 'Состояние');
  let alive = true;
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
    content.replaceChildren(
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
          ? st.problems.map((p) => h('div', { class: 'mod' }, h('span', { class: 'mark', style: { background: 'var(--yellow)' } }), h('span', { class: 'grow' }, p.text), h('span', { class: 'muted small' }, ago(p.since))))
          : h('div', { class: 'muted' }, 'Проблем нет')),
    );
  }

  function moduleRow(st, id, name) {
    const m = st.modules.find((x) => x.name === id);
    if (!m) return row('var(--faint)', name, 'ещё не сделан', 'var(--faint)');
    const [text, color] = MODULE_STATE[m.state] || [m.state, 'var(--yellow)'];
    let detail = text;
    if (id === 'ratings' && m.state === 'running') {
      const k = st.kinopoisk;
      detail = !k.keySet ? 'ключ не задан' : k.badKey ? 'ключ не подошёл' : `${k.dailyUsed} из ${k.dailyLimit} запросов за сутки`;
      if (!k.keySet || k.badKey) return row('var(--yellow)', name, detail, 'var(--yellow)');
    }
    if (m.lastError && m.state !== 'running') detail += ' · ' + m.lastError;
    return row(color, name, detail, color === 'var(--green)' ? 'var(--muted)' : color);
  }

  function trackerRow(name, t) {
    if (!t) return row('var(--faint)', name, 'нет данных', 'var(--muted)');
    const ok = t.state === 'ok';
    const lines = [ok ? `Работает · каталог обновлён ${ago(t.updatedAt)}` : t.text || 'не отвечает'];
    if (t.login) lines.push(t.login.text || LOGIN[t.login.state] || t.login.state);
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

function row(mark, name, text, color) {
  return h('div', { class: 'mod' },
    h('span', { class: 'mark', style: { background: mark } }),
    h('span', { class: 'mod-name', style: { color: mark === 'var(--faint)' ? 'var(--faint)' : null } }, name),
    h('span', { class: 'grow', style: { color } }, text));
}
