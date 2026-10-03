// «Настройки → Параметры»: Rutracker, Rutor (адреса вводит пользователь — этап 11a), прокси, источник поиска
// Jacred / Jackett (адрес и ключ — 11b-Д), хранение,
// плеер и формат в приоритете; в «Дополнительно» — служебные адреса трекеров и запасной ключ Кинопоиска
// (он работает без ключа — спека 11b, 5.7).
// «Сохранить» отправляет только изменённые поля; ошибка поля — под полем; не из домашней сети — только
// чтение (спека этапа 7, разделы 5.1, 6.3, 10.1 и 10.3).
import { h, icon, store } from '../ui.js';
import { appBridge } from './tvkit.js';
import { get, put, post } from '../api.js';
import { layout, remoteNote } from './settings-layout.js';
import { pickFolder } from './folders.js';

// FIELD — поле формы по названию поля в тексте ошибки сервера («Прокси: …»).
const FIELD = {
  'Адрес Rutracker': 'rtAddress',
  'Адрес API Rutracker': 'rtApi',
  'Адрес ленты Rutracker': 'rtFeed',
  'Адрес Rutor': 'rutorAddress',
  'Адрес .torrent Rutor': 'rutorDownload',
  'Прокси': 'proxyAddress',
  'Адрес источника поиска': 'searchAddress',
  'Хранить, дней': 'keepDays',
  'Запас места, ГБ': 'minFreeGB',
  'Раздача, МБ/с': 'upload',
  'Серий позади при нехватке места': 'keepBehind',
  'Папка загрузок': 'downloadsDir',
  'Плеер': 'player',
  'Формат в приоритете': 'preferredFormat',
  'Порядок по умолчанию': 'catalogOrder',
};
const LOGIN = { none: 'Логин не задан', unknown: 'Вход ещё не проверялся', ok: 'Вход выполнен' };

// keyPatch — ключ в PUT /settings (Кинопоиска, источника поиска): «Стереть» — пустой ключ (Х22); введён —
// он; пусто — не менять (null).
function keyPatch(value, erase) {
  if (erase) return { key: '' };
  const v = String(value || '').trim();
  return v ? { key: v } : null;
}
export const kpKeyPatch = keyPatch;
export const searchKeyPatch = keyPatch;

export function render(root, r, ctx) {
  const saveBtn = h('button', { class: 'btn inv', type: 'submit', form: 'params', 'data-key': 'save' }, icon('save'), 'Сохранить');
  const saved = h('span', { class: 'muted small', role: 'status' });
  const wizardBtn = h('a', { class: 'btn', href: '#/setup', 'data-key': 'wizard', hidden: true }, 'Открыть мастер');
  const content = layout(root, 'params', 'Параметры', saved, wizardBtn, saveBtn);
  // Одна форма на всё: Enter в поле — «Сохранить».
  const form = h('form', { id: 'params', class: 'set-content', novalidate: true, onsubmit: (e) => {
    e.preventDefault();
    save();
  } });
  content.replaceWith(form);
  let alive = true;
  let view = null; // /settings — что сейчас сохранено
  let status = null; // /status — вход Rutracker, квота Кинопоиска, canEdit
  let loginEl = null; // строка состояния входа Rutracker: «Войти» меняет только её
  let searchResult = null; // итог «Проверить» источника поиска
  const inputs = {};
  const errs = {};

  async function load() {
    try {
      [view, status] = await Promise.all([get('/settings'), get('/status')]);
    } catch (e) {
      form.replaceChildren(h('p', { class: 'error' }, e.message));
      return;
    }
    if (alive) draw();
  }
  load();

  const input = (key, value, attrs = {}) => {
    inputs[key] = h('input', { class: 'input', name: key, value: value ?? '', disabled: !status.canEdit, 'data-key': key, ...attrs });
    return inputs[key];
  };
  const secret = (key, isSet) => input(key, '', { type: 'password', placeholder: isSet ? 'задан' : '', autocomplete: 'new-password' });
  const field = (label, key, control) => {
    errs[key] = h('div', { class: 'error field-error' });
    return h('label', { class: 'fld' }, label, control, errs[key]);
  };

  function draw() {
    const v = view;
    const canEdit = status.canEdit;
    const login = status.trackers.rutracker && status.trackers.rutracker.login;
    const kp = status.kinopoisk;
    const proxyType = h('div', { class: 'seg', role: 'radiogroup', 'aria-label': 'Тип прокси' },
      [['none', 'Нет'], ['http', 'HTTP'], ['socks5', 'SOCKS5']].map(([id, t]) => {
        const radio = h('input', { type: 'radio', name: 'ptype', value: id, checked: v.proxy.type === id, disabled: !canEdit, 'data-key': `ptype-${id}`,
          onchange: () => markSeg(proxyType) });
        return h('label', { class: v.proxy.type === id ? 'on' : null }, t, radio);
      }));
    inputs.proxyType = proxyType;
    const upload = input('upload', v.storage.uploadLimitMBps === null ? '' : String(v.storage.uploadLimitMBps).replace('.', ','), { inputmode: 'decimal' });
    const uploadHint = h('div', { class: 'small hint' });
    const hint = () => {
      const t = upload.value.trim();
      uploadHint.textContent = t === '' ? 'Без ограничения' : t === '0' ? 'Раздача выключена' : '';
    };
    upload.addEventListener('input', hint);
    hint();
    const players = h('div', { class: 'checks', role: 'radiogroup', 'aria-label': 'Плеер' },
      [['auto', 'Авто'], ['vlc', 'VLC'], ['mpc-hc', 'MPC-HC']].map(([id, t]) => h('label', { class: 'check' },
        h('input', { type: 'radio', name: 'player', value: id, checked: v.player === id, disabled: !canEdit, 'data-key': `player-${id}` }), t)));
    inputs.player = players;
    errs.player = h('div', { class: 'error field-error' });
    const pf = v.catalog.preferredFormat || '';
    const formats = h('div', { class: 'checks', role: 'radiogroup', 'aria-label': 'Формат в приоритете' },
      [['', 'Нет'], ['MKV', 'MKV'], ['MP4', 'MP4'], ['AVI', 'AVI']].map(([id, t]) => h('label', { class: 'check' },
        h('input', { type: 'radio', name: 'pformat', value: id, checked: pf === id, disabled: !canEdit, 'data-key': `pformat-${id || 'none'}` }), t)));
    inputs.preferredFormat = formats;
    errs.preferredFormat = h('div', { class: 'error field-error' });
    // Порядок разделов каталога по умолчанию (план 14Б): в разделе его можно сменить, выбор запоминается.
    const co = v.catalog.order || 'seeders';
    const orders = h('div', { class: 'checks', role: 'radiogroup', 'aria-label': 'Порядок по умолчанию' },
      [['seeders', 'Раздающие'], ['leechers', 'Качающие'], ['new', 'Новые'], ['downloads', 'Скачивания']].map(([id, t]) => h('label', { class: 'check' },
        h('input', { type: 'radio', name: 'corder', value: id, checked: co === id, disabled: !canEdit, 'data-key': `order-${id}` }), t)));
    inputs.catalogOrder = orders;
    errs.catalogOrder = h('div', { class: 'error field-error' });
    errs.general = h('div', { class: 'error' });
    const loginBtn = h('button', { class: 'btn', type: 'button', disabled: !canEdit, 'data-key': 'login', onclick: relogin }, icon('login'), 'Войти');
    // «Дополнительно» — служебные адреса трекеров (пусто — по адресу сайта) и запасной ключ Кинопоиска.
    // Открыто, если что-то задано.
    const erase = v.kinopoisk.keySet && canEdit
      ? h('button', { class: 'btn', type: 'button', 'data-key': 'kp-erase', onclick: () => eraseKey('kinopoisk') }, icon('delete'), 'Стереть') : null;
    const searchErase = v.search.keySet && canEdit
      ? h('button', { class: 'btn', type: 'button', 'data-key': 'search-erase', onclick: () => eraseKey('search') }, icon('delete'), 'Стереть') : null;
    searchResult = h('div', { class: 'small check-result', role: 'status' });
    const extra = h('div', { class: 'col', hidden: !(v.rutor.downloadAddress || v.rutracker.apiAddress || v.rutracker.feedAddress || v.kinopoisk.keySet) },
      field('Адрес .torrent Rutor', 'rutorDownload', input('rutorDownload', v.rutor.downloadAddress)),
      field('Адрес API Rutracker', 'rtApi', input('rtApi', v.rutracker.apiAddress)),
      field('Адрес ленты Rutracker', 'rtFeed', input('rtFeed', v.rutracker.feedAddress)),
      field('Ключ API Кинопоиска', 'kpKey', h('div', { class: 'row gap10' }, secret('kpKey', v.kinopoisk.keySet), erase)),
      kp.badKey ? h('div', { class: 'small', style: { color: 'var(--yellow)' } }, 'Ключ не подошёл')
        : kp.keySet ? h('div', { class: 'muted small' }, `${kp.dailyUsed} из ${kp.dailyLimit} запросов за сутки`) : '');
    const extraBtn = h('button', { class: 'link', type: 'button', 'data-key': 'extra', 'aria-expanded': String(!extra.hidden), onclick: (e) => {
      extra.hidden = !extra.hidden;
      e.currentTarget.setAttribute('aria-expanded', String(!extra.hidden));
    } }, 'Дополнительно');
    const browse = h('button', { class: 'btn', type: 'button', disabled: !canEdit, 'data-key': 'browse-downloads', onclick: async () => {
      const p = await pickFolder(inputs.downloadsDir.value.trim());
      if (p) inputs.downloadsDir.value = p;
    } }, icon('folder_open'), 'Обзор');
    wizardBtn.hidden = !canEdit;

    form.replaceChildren(
      canEdit ? '' : remoteNote(),
      errs.general,
      h('div', { class: 'two-cols' },
        h('div', { class: 'col' },
          h('div', { class: 'card' }, h('div', { class: 'h' }, 'Rutracker'),
            field('Адрес сайта', 'rtAddress', input('rtAddress', v.rutracker.address, { autocomplete: 'off', inputmode: 'url' })),
            h('div', { class: 'two' }, field('Логин', 'rtLogin', input('rtLogin', v.rutracker.login, { autocomplete: 'off' })), field('Пароль', 'rtPassword', secret('rtPassword', v.rutracker.passwordSet))),
            h('div', { class: 'row gap10' }, (loginEl = loginLine(login)), loginBtn)),
          h('div', { class: 'card' }, h('div', { class: 'h' }, 'Rutor'),
            field('Адрес сайта или зеркала', 'rutorAddress', input('rutorAddress', v.rutor.address, { autocomplete: 'off', inputmode: 'url' }))),
          h('div', { class: 'card' }, h('div', { class: 'h' }, 'Прокси для трекеров'), proxyType,
            field('Адрес', 'proxyAddress', input('proxyAddress', v.proxy.address, { placeholder: '192.168.1.20:3128' })),
            h('div', { class: 'two' }, field('Логин', 'proxyLogin', input('proxyLogin', v.proxy.login, { autocomplete: 'off' })), field('Пароль', 'proxyPassword', secret('proxyPassword', v.proxy.passwordSet)))),
          h('div', { class: 'card' }, h('div', { class: 'h' }, 'Источник поиска'),
            field('Адрес', 'searchAddress', input('searchAddress', v.search.address, { autocomplete: 'off', inputmode: 'url' })),
            field('Ключ', 'searchKey', h('div', { class: 'row gap10' }, secret('searchKey', v.search.keySet), searchErase)),
            h('div', { class: 'row gap10' },
              h('button', { class: 'btn', type: 'button', disabled: !canEdit, 'data-key': 'search-check', onclick: checkSearch }, icon('network_check'), 'Проверить'),
              searchResult)),
          h('div', { class: 'card' }, extraBtn, extra)),
        h('div', { class: 'col' },
          h('div', { class: 'card' }, h('div', { class: 'h' }, 'Хранение'),
            field('Папка загрузок', 'downloadsDir', h('div', { class: 'row gap10' }, input('downloadsDir', v.storage.downloadsDir), browse)),
            h('div', { class: 'three' },
              field('Хранить, дней', 'keepDays', input('keepDays', String(v.storage.keepDays), { inputmode: 'numeric' })),
              field('Запас места, ГБ', 'minFreeGB', input('minFreeGB', String(v.storage.minFreeGB), { inputmode: 'numeric' })),
              field('Раздача, МБ/с', 'upload', upload)),
            uploadHint,
            field('Серий позади при нехватке места', 'keepBehind', input('keepBehind', String(v.storage.keepBehind), { inputmode: 'numeric' }))),
          deviceCard(),
          h('div', { class: 'card' }, h('div', { class: 'h' }, 'Плеер'), players, errs.player),
          h('div', { class: 'card' }, h('div', { class: 'h' }, 'Каталог'),
            h('div', { class: 'fld' }, 'Порядок по умолчанию', orders, errs.catalogOrder),
            h('div', { class: 'fld' }, 'Формат в приоритете', formats, errs.preferredFormat)))),
    );
    saveBtn.disabled = !canEdit;
  }

  // «На этом устройстве» (план 18Б): где смотреть фильмы — выбор этого браузера; в приложении — своя настройка.
  function deviceCard() {
    if (appBridge()) return null;
    const now = store.get('moviePlayer') === 'external' ? 'external' : 'web';
    return h('div', { class: 'card' }, h('div', { class: 'h' }, 'На этом устройстве'),
      h('div', { class: 'fld' }, 'Фильмы',
        h('div', { class: 'checks', role: 'radiogroup', 'aria-label': 'Фильмы' },
          [['web', 'В браузере'], ['external', 'Во внешнем плеере']].map(([id, t]) => h('label', null,
            h('input', { type: 'radio', name: 'movie-player', value: id, checked: now === id, 'data-key': `movie-${id}`,
              onchange: () => store.set('moviePlayer', id) }), t)))));
  }

  // eraseKey — «Стереть» ключ: Кинопоиска (без него — без токена) или источника поиска (group).
  async function eraseKey(group) {
    errs.general.textContent = '';
    try {
      view = await put('/settings', { [group]: keyPatch('', true) });
      status = await get('/status');
    } catch (e) {
      errs.general.textContent = e.message;
      return;
    }
    if (alive) draw();
  }

  // checkSearch — «Проверить» источник поиска: изменённые адрес и ключ сначала сохраняются — проверяется
  // сохранённое (как «Проверить» в мастере).
  async function checkSearch(e) {
    const btn = e.currentTarget;
    const el = searchResult;
    btn.disabled = true;
    errs.searchAddress.textContent = errs.general.textContent = '';
    try {
      const p = {};
      const addr = inputs.searchAddress.value.trim();
      if (addr !== view.search.address) p.address = addr;
      const key = searchKeyPatch(inputs.searchKey.value, false);
      if (key) p.key = key.key;
      if (Object.keys(p).length > 0) {
        view = await put('/settings', { search: p });
        inputs.searchAddress.value = view.search.address; // как сохранил сервер: «схема://хост»
        inputs.searchKey.value = '';
        inputs.searchKey.placeholder = view.search.keySet ? 'задан' : '';
      }
      el.textContent = 'Проверяю…';
      el.classList.remove('good', 'bad');
      const res = await post('/setup/check', { source: 'search' });
      if (!alive) return;
      el.textContent = res.text;
      el.classList.toggle('good', res.ok);
      el.classList.toggle('bad', !res.ok);
    } catch (x) {
      const at = x.message.indexOf(':');
      const k = at > 0 ? FIELD[x.message.slice(0, at)] : null;
      (k && errs[k] ? errs[k] : errs.general).textContent = x.message;
      el.textContent = '';
    }
    btn.disabled = !status.canEdit;
  }

  // markSeg — выбранный тип прокси подсвечен.
  function markSeg(seg) {
    for (const l of seg.querySelectorAll('label')) l.classList.toggle('on', l.querySelector('input').checked);
  }

  function loginLine(login) {
    if (!login) return h('div', { class: 'grow' });
    if (login.state === 'blocked' || login.state === 'failing') return h('div', { class: 'warn grow' }, icon('warning'), login.text || 'вход не выполнен');
    return h('div', { class: 'grow login-state', style: { color: login.state === 'ok' ? 'var(--green)' : 'var(--muted)' } }, LOGIN[login.state] || login.state);
  }

  // patch — только изменённые поля (спека этапа 7, раздел 5.1). Прокси — всегда тип вместе с
  // адресом: адрес без типа сервер понял бы как «нет прокси».
  function patch() {
    const v = view;
    const p = {};
    const bad = {}; // поле → ошибка, найденная в пульте: неверное число сервер не разобрал бы
    const val = (k) => inputs[k].value.trim();
    const set = (group, key, value) => {
      p[group] = p[group] || {};
      p[group][key] = value;
    };
    for (const [k, group, key, was] of [['rtAddress', 'rutracker', 'address', v.rutracker.address], ['rtApi', 'rutracker', 'apiAddress', v.rutracker.apiAddress],
      ['rtFeed', 'rutracker', 'feedAddress', v.rutracker.feedAddress], ['rutorAddress', 'rutor', 'address', v.rutor.address],
      ['rutorDownload', 'rutor', 'downloadAddress', v.rutor.downloadAddress]]) {
      if (val(k) !== was) set(group, key, val(k));
    }
    if (val('rtLogin') !== v.rutracker.login) set('rutracker', 'login', val('rtLogin'));
    if (val('rtPassword')) set('rutracker', 'password', inputs.rtPassword.value);
    const ptype = inputs.proxyType.querySelector('input:checked').value;
    if (ptype !== v.proxy.type || val('proxyAddress') !== v.proxy.address || val('proxyLogin') !== v.proxy.login || val('proxyPassword')) {
      p.proxy = { type: ptype, address: val('proxyAddress'), login: val('proxyLogin') };
      if (val('proxyPassword')) p.proxy.password = inputs.proxyPassword.value;
    }
    const kpk = kpKeyPatch(val('kpKey'), false);
    if (kpk) p.kinopoisk = kpk;
    if (val('searchAddress') !== v.search.address) set('search', 'address', val('searchAddress'));
    const sk = searchKeyPatch(val('searchKey'), false);
    if (sk) set('search', 'key', sk.key);
    if (val('downloadsDir') !== v.storage.downloadsDir) set('storage', 'downloadsDir', val('downloadsDir'));
    for (const [k, min, text] of [['keepDays', 1, 'Хранить, дней: нужно целое число от 1'], ['minFreeGB', 0, 'Запас места, ГБ: нужно целое число от 0'],
      ['keepBehind', 0, 'Серий позади при нехватке места: нужно целое число от 0']]) {
      if (val(k) === String(v.storage[k])) continue;
      const n = number(val(k));
      if (n === null || !Number.isInteger(n) || n < min) bad[k] = text;
      else set('storage', k, n);
    }
    const up = val('upload');
    const was = v.storage.uploadLimitMBps === null ? '' : String(v.storage.uploadLimitMBps).replace('.', ',');
    if (up !== was) {
      const n = up === '' ? null : number(up);
      if (up !== '' && (n === null || n < 0)) bad.upload = 'Раздача, МБ/с: нужно число от 0 или пустое поле';
      else set('storage', 'uploadLimitMBps', n);
    }
    const player = inputs.player.querySelector('input:checked').value;
    if (player !== v.player) p.player = player;
    const format = inputs.preferredFormat.querySelector('input:checked').value;
    if (format !== (v.catalog.preferredFormat || '')) set('catalog', 'preferredFormat', format);
    const order = inputs.catalogOrder.querySelector('input:checked').value;
    if (order !== (v.catalog.order || 'seeders')) set('catalog', 'order', order);
    return { p, bad };
  }

  async function save() {
    for (const e of Object.values(errs)) e.textContent = '';
    saved.textContent = '';
    const { p, bad } = patch();
    if (Object.keys(bad).length > 0) {
      for (const [k, text] of Object.entries(bad)) errs[k].textContent = text;
      return;
    }
    if (Object.keys(p).length === 0) {
      saved.textContent = 'Нечего сохранять';
      return;
    }
    saveBtn.disabled = true;
    try {
      view = await put('/settings', p);
      status = await get('/status');
      draw();
      saved.textContent = 'Сохранено';
      ctx.refreshStatus();
    } catch (e) {
      const at = e.message.indexOf(':');
      const key = at > 0 ? FIELD[e.message.slice(0, at)] : null;
      (key && errs[key] ? errs[key] : errs.general).textContent = e.message;
      saveBtn.disabled = false;
    }
  }

  // relogin — «Войти»: снять запрет входа и попробовать ещё раз (спека этапа 7, раздел 5.3).
  // Логин или пароль изменили на форме — сначала они сохраняются, иначе вход шёл бы со старой парой.
  // Перерисовывается только строка входа: остальное введённое на форме не теряется.
  async function relogin(e) {
    const btn = e.currentTarget;
    const hadFocus = document.activeElement === btn;
    btn.disabled = true;
    errs.general.textContent = '';
    try {
      const creds = {};
      if (inputs.rtLogin.value.trim() !== view.rutracker.login) creds.login = inputs.rtLogin.value.trim();
      if (inputs.rtPassword.value) creds.password = inputs.rtPassword.value;
      if (Object.keys(creds).length > 0) {
        view = await put('/settings', { rutracker: creds });
        inputs.rtLogin.value = view.rutracker.login;
        inputs.rtPassword.value = '';
        inputs.rtPassword.placeholder = view.rutracker.passwordSet ? 'задан' : '';
      }
      await post('/sources/rutracker/login');
      status = await get('/status');
    } catch (err) {
      errs.general.textContent = err.message;
    }
    if (!alive) return;
    const next = loginLine(status.trackers.rutracker && status.trackers.rutracker.login);
    loginEl.replaceWith(next);
    loginEl = next;
    btn.disabled = !status.canEdit;
    if (hadFocus && document.activeElement === document.body) btn.focus({ preventScroll: true });
    ctx.refreshStatus();
  }

  return () => {
    alive = false;
  };
}

// number — число из поля, запятая — как точка; не число — null.
function number(s) {
  const n = Number(s.replace(',', '.'));
  return s === '' || !Number.isFinite(n) ? null : n;
}
