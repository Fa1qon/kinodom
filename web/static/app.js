// Пульт Kinodom: маршруты по адресу после «#», шапка, отметка проблем в меню (спека этапа 7, раздел 6).
import { h, icon, clear, poll, store, carryFocus } from './ui.js';
import { NAV } from './app-nav.js';
import { get } from './api.js';
import { initNav } from './nav.js';
import * as catalog from './views/catalog.js';
import { catalogHome } from './views/catalog.js';
import * as release from './views/release.js';
import * as search from './views/search.js';
import * as downloads from './views/downloads.js';
import * as settingsStatus from './views/settings-status.js';
import * as settingsParams from './views/settings-params.js';
import * as settingsSections from './views/settings-sections.js';
import * as channels from './views/channels.js';
import * as history from './views/history.js';
import * as library from './views/library.js';
import * as settingsLibrary from './views/settings-library.js';
import * as channel from './views/channel.js';
import * as settingsIPTV from './views/settings-iptv.js';
import * as settingsUnrecognized from './views/settings-unrecognized.js';
import * as settingsApp from './views/settings-app.js';
import * as setup from './views/setup.js';
import * as updates from './views/updates.js';
import { settingsRoute } from './views/settings-layout.js';

// views — экраны по первой части адреса; у «Настроек» — по второй.
const views = { catalog, release, search, downloads, channels, channel, history, library, setup, updates };
const settingsViews = { status: settingsStatus, params: settingsParams, sections: settingsSections, iptv: settingsIPTV, unrecognized: settingsUnrecognized,
  library: settingsLibrary, app: settingsApp };

// ctx — общее для экранов: последнее «Состояние» и переходы. local — пульт открыт на ПК с Kinodom
// (плеер по ссылке kinodom://), canEdit — из домашней сети: можно менять настройки и удалять.
const ctx = {
  status: null, // последний ответ /status; null — ещё не пришёл или сервер не ответил
  get local() {
    return !!(this.status && this.status.local);
  },
  get canEdit() {
    return !!(this.status && this.status.canEdit);
  },
  go(hash) {
    location.hash = hash;
  },
  listeners: new Set(), // экраны, которым нужно свежее «Состояние»: fn(status)
  prev: '', // адрес (без «?…») до последнего перехода; '' — первый экран
  refreshStatus: () => statusPoll && statusPoll.now(),
};
let statusPoll = null;

// route — разбор адреса: «#/catalog/rutor/12?page=2» → {parts: [catalog, rutor, 12], query}.
function route() {
  const hash = location.hash.replace(/^#\/?/, '');
  const [p, q = ''] = hash.split('?');
  return { parts: p.split('/').filter(Boolean).map(decodeURIComponent), query: new URLSearchParams(q) };
}

function defaultRoute() {
  return catalogHome(store.get('catalog'));
}


const top = document.getElementById('top');
const navLinks = {};
let searchInput;
let menuButton;
let bell; // колокольчик «Новые серии» (спека 11b, 6.1)
let bellCount;

// buildHeader — шапка: логотип, меню, поиск; на узком экране — значки поиска и меню. Строится один раз: опрос «Состояния» не должен сбивать то, что человек
// вводит в поиск.
function buildHeader() {
  const nav = h('nav', { class: 'nav', id: 'nav', 'aria-label': 'Разделы' },
    NAV.map(([id, title, soon]) => {
      if (soon) return h('span', { class: 'soon', 'aria-disabled': 'true' }, title);
      navLinks[id] = h('a', { href: id === 'settings' ? '#/settings/status' : '#/' + id }, title);
      return navLinks[id];
    }));
  searchInput = h('input', { name: 'q', placeholder: 'Поиск', 'aria-label': 'Поиск', autocomplete: 'off', enterkeyhint: 'search' });
  const search = h('form', { class: 'search', role: 'search', onsubmit: (e) => {
    e.preventDefault();
    const q = searchInput.value.trim();
    if (q) ctx.go('#/search?q=' + encodeURIComponent(q));
  } }, h('label', { class: 'field' }, icon('search'), searchInput));
  menuButton = h('button', { class: 'sq narrow-only', type: 'button', 'aria-label': 'Меню', 'aria-controls': 'nav', 'aria-expanded': 'false',
    onclick: () => setMenu(!top.classList.contains('open')) }, icon('menu'));
  bellCount = h('span', { class: 'bell-n' });
  bell = h('a', { class: 'sq bell', href: '#/updates', 'aria-label': 'Новые серии', 'data-key': 'bell' }, icon('notifications'), bellCount);
  top.append(
    // Логотип заказчика: иконка, затем надпись (нарезка — assets/logo/cut.py).
    h('a', { class: 'logo', href: '#/', 'aria-label': 'Kinodom' },
      h('img', { class: 'logo-icon', src: 'logo-icon.png', alt: '' }), h('img', { class: 'logo-text', src: 'logo-text.png', alt: '' })),
    nav,
    h('div', { class: 'grow' }),
    bell,
    search,
    h('a', { class: 'sq narrow-only', href: '#/search', 'aria-label': 'Поиск' }, icon('search')),
    menuButton,
  );
}

function setMenu(open) {
  top.classList.toggle('open', open);
  menuButton.setAttribute('aria-expanded', String(open));
  menuButton.replaceChildren(icon(open ? 'close' : 'menu'));
}

// updateHeader — выбранный раздел и жёлтый значок у «Настроек», если есть хоть одна проблема.
function updateHeader(r) {
  const current = { release: 'catalog', channel: 'channels' }[r.parts[0]] || r.parts[0];
  navLinks.catalog.href = defaultRoute();
  for (const [id, a] of Object.entries(navLinks)) {
    a.classList.toggle('on', id === current);
    if (id === current) a.setAttribute('aria-current', 'page');
    else a.removeAttribute('aria-current');
  }
  const settings = navLinks.settings;
  settings.querySelector('.ic')?.remove();
  if (ctx.status && ctx.status.problems.length > 0) settings.append(icon('warning', 18, 'Есть проблемы'));
  const n = updates.bellText(ctx.status && ctx.status.updates);
  bellCount.textContent = n;
  bell.classList.toggle('on', r.parts[0] === 'updates');
  bell.setAttribute('aria-label', n ? `Новые серии: ${n}` : 'Новые серии');
}

let cleanup = null;
let lastView = null;

function render() {
  const r = route();
  if (r.parts.length === 0) {
    location.replace(defaultRoute());
    return;
  }
  const [first] = r.parts;
  let view = views[first];
  if (first === 'settings') {
    const sr = settingsRoute(r.parts.slice(1));
    if (sr.redirect) {
      const q = r.query.toString();
      location.replace(sr.redirect + (q ? '?' + q : ''));
      return;
    }
    view = settingsViews[sr.view];
  }
  updateHeader(r);
  searchInput.value = first === 'search' ? r.query.get('q') || '' : '';
  if (cleanup) cleanup();
  cleanup = null;
  const main = document.getElementById('view');
  // Ключ нажатого (вкладка, пункт меню) — на новый экран: на ТВ фокус не уходит в начало страницы.
  const active = document.activeElement;
  const key = active && main.contains(active) && active.dataset ? active.dataset.key : null;
  clear(main);
  if (!view) {
    main.append(h('p', { class: 'empty' }, 'Такой страницы нет'));
    return;
  }
  if (view !== lastView) window.scrollTo(0, 0);
  lastView = view;
  cleanup = view.render(main, r, ctx) || null;
  carryFocus(main, key);
}

buildHeader();
initNav();
window.addEventListener('hashchange', (e) => {
  const at = e.oldURL.indexOf('#');
  ctx.prev = at < 0 ? '' : e.oldURL.slice(at).split('?')[0];
  setMenu(false);
  render();
});
render();

// «Состояние» раз в 15 с: отметка проблем в меню и свежие данные экранам (спека этапа 7, раздел 4).
// setupChecked — мастер начальных настроек предлагается один раз за загрузку страницы: переходы из
// мастера в каталог не возвращают в него (спека этапа 11a, раздел 7).
let setupChecked = false;
statusPoll = poll(async () => {
  try {
    ctx.status = await get('/status');
  } catch {
    ctx.status = null;
  }
  if (!setupChecked && ctx.status) {
    setupChecked = true;
    if (setup.shouldOpenSetup(ctx.status, location.hash)) location.replace('#/setup');
  }
  updateHeader(route());
  for (const fn of ctx.listeners) fn(ctx.status);
}, 15000);
