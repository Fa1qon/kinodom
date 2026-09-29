// Раскладка «Настроек»: слева — подразделы «Состояние», «Параметры», «Разделы каталога» (спека этапа 7,
// раздел 6.3), «Каналы» и «Не распознано» (спека этапа 8, раздел 6).
import { h, icon } from '../ui.js';

const ITEMS = [
  ['status', 'Состояние', 'dashboard'],
  ['params', 'Параметры', 'tune'],
  ['sections', 'Разделы каталога', 'account_tree'],
  ['iptv', 'Каналы', 'live_tv'],
  ['unrecognized', 'Не распознано', 'help_outline'],
];

// layout — каркас подраздела; возвращает место для содержимого. actions — кнопки справа от заголовка.
export function layout(root, active, title, ...actions) {
  const content = h('div', { class: 'set-content' });
  root.append(h('div', { class: 'screen' }, h('div', { class: 'set-grid' },
    h('nav', { class: 'side', 'aria-label': 'Разделы настроек' },
      ITEMS.map(([id, t, ic]) => h('a', { href: `#/settings/${id}`, class: id === active ? 'on' : null, 'aria-current': id === active ? 'page' : null }, icon(ic), t))),
    h('div', { class: 'set-main' }, h('div', { class: 'row' }, h('h1', { class: 'grow' }, title), ...actions), content))));
  return content;
}

// remoteNote — не из домашней сети настройки только читаются (спека этапа 7, раздел 10.1).
export function remoteNote() {
  return h('div', { class: 'note' }, icon('warning'), 'Изменить можно только из домашней сети');
}
