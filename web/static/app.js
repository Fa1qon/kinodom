// Пульт Kinodom: маршруты по адресу после «#», шапка, отметка проблем в меню (спека этапа 7, раздел 6).
import { h, icon, clear, poll, store } from './ui.js';
import { get } from './api.js';
import * as catalog from './views/catalog.js';

// views — экраны по первой части адреса; у «Настроек» — по второй.
const views = { catalog };
const settingsViews = {};

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
  return store.get('catalog') || '#/catalog/rutor';
}

const NAV = [
  ['catalog', 'Каталог'],
  ['channels', 'Каналы', true],
  ['library', 'Медиатека', true],
  ['downloads', 'Загрузки'],
  ['settings', 'Настройки'],
];

const top = document.getElementById('top');
const navLinks = {};
let searchInput;
let menuButton;

// buildHeader — шапка: логотип, меню, поиск; на узком экране — значки поиска и меню. «Каналы» и
// «Медиатека» неактивны до своих этапов. Строится один раз: опрос «Состояния» не должен сбивать
// то, что человек вводит в поиск.
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
  top.append(
    h('a', { class: 'logo', href: '#/' }, 'Kinodom'),
    nav,
    h('div', { class: 'grow' }),
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
  const current = r.parts[0] === 'release' ? 'catalog' : r.parts[0];
  navLinks.catalog.href = defaultRoute();
  for (const [id, a] of Object.entries(navLinks)) {
    a.classList.toggle('on', id === current);
    if (id === current) a.setAttribute('aria-current', 'page');
    else a.removeAttribute('aria-current');
  }
  const settings = navLinks.settings;
  settings.querySelector('.ic')?.remove();
  if (ctx.status && ctx.status.problems.length > 0) settings.append(icon('warning', 18, 'Есть проблемы'));
}

let cleanup = null;
let lastView = null;

function render() {
  const r = route();
  if (r.parts.length === 0) {
    location.replace(defaultRoute());
    return;
  }
  const [first, second] = r.parts;
  const view = first === 'settings' ? settingsViews[second || 'status'] : views[first];
  updateHeader(r);
  searchInput.value = first === 'search' ? r.query.get('q') || '' : '';
  if (cleanup) cleanup();
  cleanup = null;
  const main = document.getElementById('view');
  clear(main);
  if (!view) {
    main.append(h('p', { class: 'empty' }, 'Такой страницы нет'));
    return;
  }
  if (view !== lastView) window.scrollTo(0, 0);
  lastView = view;
  cleanup = view.render(main, r, ctx) || null;
}

buildHeader();
window.addEventListener('hashchange', () => {
  setMenu(false);
  render();
});
render();

// «Состояние» раз в 15 с: отметка проблем в меню и свежие данные экранам (спека этапа 7, раздел 4).
statusPoll = poll(async () => {
  try {
    ctx.status = await get('/status');
  } catch {
    ctx.status = null;
  }
  updateHeader(route());
  for (const fn of ctx.listeners) fn(ctx.status);
}, 15000);
