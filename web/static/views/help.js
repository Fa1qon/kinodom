// Справка «?» из шапки: веб-версия и приложение используют разные способы управления,
// поэтому переключаются отдельными вкладками. Открыл — закрыл «Понятно», Esc или «Назад».
import { h, openModal } from '../ui.js';
import { isClient } from '../client-ui.js';

const SCREENS = [
  ['Экраны', [
    ['Каталог', 'раздачи трекеров: постеры по порядку, панель выбора слева'],
    ['Поиск', 'найти фильм или сериал по названию'],
    ['Каналы', 'ТВ: плейлисты, программа, «Смотреть» прямо здесь'],
    ['Медиатека', 'ваши папки с фильмами и сериалами'],
    ['Загрузки', 'что качается и сколько осталось'],
    ['История', 'что и где смотрели — «Продолжить» отсюда'],
    ['Настройки', 'трекеры, каталог, каналы, медиатека, приложение'],
  ]],
];

const WEB_ROWS = [
  ...SCREENS,
  ['Веб-плеер', [
    ['Кнопки', 'пауза, список серий, озвучка, субтитры и полный экран'],
    ['Шкала', 'перетащите ползунок для перемотки'],
    ['Громкость', 'наведите мышь на плеер и прокрутите колесо'],
    ['Меню дорожек', 'повторная кнопка или клик вне меню закрывает список'],
  ]],
  ['Мышью и пальцем', [
    ['Нажатие', 'открывает карточку или кнопку'],
    ['Плеер', 'клик — пауза, двойной клик — полный экран'],
  ]],
];

const APP_ROWS = [
  ...SCREENS,
  ['С пульта ТВ', [
    ['Стрелки', 'перемещение по экрану'],
    ['OK', 'открыть карточку или нажать кнопку'],
    ['Назад', 'закрыть или шаг назад'],
    ['Каталог', 'кнопка-сводка сверху — панель «трекер · раздел · порядок»'],
    ['Плеер фильма', 'список серий — выбор эпизода, шкала — перемотка, ↓ — кнопки'],
    ['Плеер канала', '↑/↓ — каналы, цифры — номер, OK — список, «Инфо» — передача'],
  ]],
  ['Телефон', [
    ['Нажатие', 'открывает карточку или кнопку, как на ТВ'],
    ['Поля настроек', 'сначала выберите поле, затем нажмите для вызова клавиатуры'],
    ['Плеер', 'касание показывает кнопки, шкала тянется пальцем'],
  ]],
];

const MODES = {
  web: { label: 'Веб-версия', rows: WEB_ROWS },
  app: { label: 'Приложение', rows: APP_ROWS },
};

export function openHelp() {
  const closeBtn = h('button', { class: 'btn inv', type: 'button', 'data-key': 'help-close' }, 'Понятно');
  const modeTabs = h('div', { class: 'tabs help-modes', role: 'tablist', 'aria-label': 'Версия Kinodom' });
  const content = h('div', { class: 'help-content' });
  const box = h('div', { class: 'dlg card help', role: 'dialog', 'aria-modal': 'true', 'aria-label': 'Справка' },
    h('div', { class: 'h' }, 'Как пользоваться Kinodom'),
    modeTabs, content,
    h('div', { class: 'row gap10' }, closeBtn));
  let mode = isClient() ? 'app' : 'web';

  function selectHelpMode(next) {
    mode = next;
    modeTabs.replaceChildren(...Object.entries(MODES).map(([id, item]) => h('button', {
      class: id === mode ? 'on' : '', type: 'button', role: 'tab', 'aria-selected': String(id === mode), 'data-key': `help-${id}`,
      onclick: () => selectHelpMode(id),
    }, item.label)));
    content.replaceChildren(...MODES[mode].rows.map(([title, rows]) => h('div', { class: 'help-part' }, h('div', { class: 'help-title' }, title),
      h('div', { class: 'help-rows' }, rows.map(([k, v]) => h('div', { class: 'help-row' },
        h('span', { class: 'help-key' }, k), h('span', { class: 'help-val' }, v)))))));
  }
  selectHelpMode(mode);
  const modal = openModal(box, close);
  function close() {
    modal.close();
  }
  closeBtn.onclick = close;
  closeBtn.focus({ preventScroll: true });
}
