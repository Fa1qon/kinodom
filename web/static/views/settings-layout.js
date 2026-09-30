// Раскладка «Настроек»: слева — подразделы «Состояние», «Параметры», «Разделы каталога» (спека этапа 7,
// раздел 6.3), «Каналы» с вкладкой «Не распознано» (спека этапа 8, раздел 6; этап 11b, замечание № 5),
// «Медиатека» (спека этапа 9, раздел 6.3).
import { h, icon } from '../ui.js';

const ITEMS = [
  ['status', 'Состояние', 'dashboard'],
  ['params', 'Параметры', 'tune'],
  ['sections', 'Разделы каталога', 'account_tree'],
  ['iptv', 'Каналы', 'live_tv'],
  ['library', 'Медиатека', 'video_library'],
];

// settingsRoute — экран настроек по адресу после «settings»: {view} или {redirect} — старый адрес
// «Не распознано» (до этапа 11b) ведёт на вкладку «Каналов».
export function settingsRoute(parts) {
  const [first = 'status', second] = parts;
  if (first === 'unrecognized') return { redirect: '#/settings/iptv/unrecognized' };
  if (first === 'iptv' && second === 'unrecognized') return { view: 'unrecognized' };
  return { view: first };
}

// channelTabs — вкладки «Настроек → Каналы»: «Каналы» и «Не распознано».
export function channelTabs(active) {
  return h('nav', { class: 'tabs', 'aria-label': 'Каналы' },
    [['iptv', 'Каналы', '#/settings/iptv'], ['unrecognized', 'Не распознано', '#/settings/iptv/unrecognized']].map(([id, t, href]) =>
      h('a', { href, class: id === active ? 'on' : null, 'aria-current': id === active ? 'page' : null, 'data-key': `tab-${id}` }, t)));
}

// layout — каркас подраздела; возвращает место для содержимого. actions — кнопки справа от заголовка.
export function layout(root, active, title, ...actions) {
  const content = h('div', { class: 'set-content' });
  root.append(h('div', { class: 'screen' }, h('div', { class: 'set-grid' },
    h('nav', { class: 'side', 'aria-label': 'Разделы настроек' },
      ITEMS.map(([id, t, ic]) => h('a', { href: `#/settings/${id}`, class: id === active ? 'on' : null, 'aria-current': id === active ? 'page' : null, 'data-key': `set-${id}` }, icon(ic), t))),
    h('div', { class: 'set-main' }, h('div', { class: 'row' }, h('h1', { class: 'grow' }, title), ...actions), content))));
  return content;
}

// remoteNote — не из домашней сети настройки только читаются (спека этапа 7, раздел 10.1).
export function remoteNote() {
  return h('div', { class: 'note' }, icon('warning'), 'Изменить можно только из домашней сети');
}
