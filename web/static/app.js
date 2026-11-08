// Пульт Kinodom: маршруты по адресу после «#», шапка, отметка проблем в меню (спека этапа 7, раздел 6).
import { h, icon, clear, poll, store, carryFocus } from './ui.js';
import { NAV } from './app-nav.js';
import { get } from './api.js';
import { initNav, reveal } from './nav.js';
import { markOwned } from './owned.js';
import { isClient, initClientUI } from './client-ui.js';
import * as catalog from './views/catalog.js';
import * as home from './views/home.js';
import * as recs from './views/recs.js';
import { catalogHome } from './views/catalog.js';
import * as release from './views/release.js';
import * as film from './views/film.js';
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
import { openHelp } from './views/help.js';
import * as updates from './views/updates.js';
import * as player from './views/player.js';
import * as channelPlayer from './views/channel-player.js';
import { settingsRoute } from './views/settings-layout.js';

// views — экраны по первой части адреса; у «Настроек» — по второй.
const views = { home, recs, catalog, release, film, search, downloads, channels, channel, history, library, setup, updates, play: player, 'channel-play': channelPlayer };
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

// route — разбор адреса: «#/catalog/t1/12?page=2» → {parts: [catalog, t1, 12], query}.
function route() {
  const hash = location.hash.replace(/^#\/?/, '');
  const [p, q = ''] = hash.split('?');
  return { parts: p.split('/').filter(Boolean).map(decodeURIComponent), query: new URLSearchParams(q) };
}

function catalogRoute() {
  return catalogHome(store.get('catalog'));
}

function defaultRoute() {
  return '#/home';
}


const top = document.getElementById('top');
const navLinks = {};
let menuButton;
let searchBtn; // «Поиск» в шапке (план 16Б)
let bell; // колокольчик «Новые серии» (спека 11b, 6.1)
let bellCount;

// buildHeader — боковое меню (как у Lampa): логотип, «Поиск», разделы, «Новые серии», «Справка»; свёрнуто — одни
// значки, с фокусом пульта или под мышью раскрывается поверх экрана. На телефоне меню выезжает по кнопке из верхней
// полосы. Поля поиска в меню нет (план 16Б): на ТВ стрелки попадали в него и вызывали клавиатуру. Строится один раз.
function buildHeader() {
  const item = (href, ic, title, key, extra) => h('a', { class: 'mi', href, 'data-key': key, 'aria-label': title, title },
    icon(ic, 24), h('span', { class: 'label' }, title), extra || null);
  searchBtn = item('#/search', 'search', 'Поиск', 'search');
  const nav = h('nav', { class: 'nav', id: 'nav', 'aria-label': 'Разделы' },
    NAV.map(([id, title, ic]) => {
      navLinks[id] = item(id === 'settings' ? '#/settings/status' : '#/' + id, ic, title, 'menu-' + id);
      return navLinks[id];
    }));
  bellCount = h('span', { class: 'bell-n' });
  bell = item('#/updates', 'notifications', 'Новые серии', 'bell', bellCount);
  bell.classList.add('bell');
  const helpBtn = h('button', { class: 'mi', type: 'button', 'aria-label': 'Справка', title: 'Справка', 'data-key': 'help', onclick: openHelp },
    icon('help_outline', 24), h('span', { class: 'label' }, 'Справка'));
  top.append(
    // Логотип заказчика: иконка, затем надпись (нарезка — assets/logo/cut.py).
    h('a', { class: 'logo', href: '#/home', 'aria-label': 'Kinodom', tabindex: '-1' },
      h('img', { class: 'logo-icon', src: 'logo-icon.png', alt: '' }), h('img', { class: 'logo-text', src: 'logo-text.png', alt: '' })),
    searchBtn,
    nav,
    bell,
    h('div', { class: 'sep' }),
    helpBtn,
  );
  menuButton = h('button', { class: 'sq', type: 'button', 'aria-label': 'Меню', 'aria-controls': 'top', 'aria-expanded': 'false',
    onclick: () => setMenu(!top.classList.contains('open')) }, icon('menu'));
  top.before(h('div', { class: 'mbar' }, menuButton,
    h('a', { class: 'logo', href: '#/home', 'aria-label': 'Kinodom' }, h('img', { class: 'logo-icon', src: 'logo-icon.png', alt: '' }))));
}

function setMenu(open) {
  top.classList.toggle('open', open);
  menuButton.setAttribute('aria-expanded', String(open));
  menuButton.replaceChildren(icon(open ? 'close' : 'menu'));
}

// updateHeader — выбранный раздел и жёлтый значок у «Настроек», если есть хоть одна проблема.
function updateHeader(r) {
  document.body.classList.toggle('playing', r.parts[0] === 'play' || r.parts[0] === 'channel-play'); // плеер — во всё окно, без шапки (план 18Б)
  const current = { release: 'catalog', channel: 'channels' }[r.parts[0]] || r.parts[0];
  navLinks.catalog.href = catalogRoute();
  for (const [id, a] of Object.entries(navLinks)) {
    a.classList.toggle('on', id === current);
    if (id === current) a.setAttribute('aria-current', 'page');
    else a.removeAttribute('aria-current');
  }
  const settings = navLinks.settings;
  settings.querySelector('.warn-dot')?.remove();
  if (ctx.status && ctx.status.problems.length > 0) {
    const w = icon('warning', 18, 'Есть проблемы');
    w.classList.add('warn-dot');
    settings.querySelector('.ic').after(w);
  }
  const n = updates.bellText(ctx.status && ctx.status.updates);
  bellCount.textContent = n;
  bell.classList.toggle('on', r.parts[0] === 'updates');
  searchBtn.classList.toggle('on', r.parts[0] === 'search');
  bell.setAttribute('aria-label', n ? `Новые серии: ${n}` : 'Новые серии');
}

let cleanup = null;
let lastView = null;
let initialClientFocus = isClient();

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
  if (initialClientFocus) {
    initialClientFocus = false;
    if (!navLinks.home.getClientRects().length) setMenu(true);
    navLinks.home.focus({ preventScroll: true });
  }
}

buildHeader();
initClientUI();
// Метки «Есть» (фильм в медиатеке) на постерах любого экрана: экраны дорисовывают карточки порциями — после
// каждой перерисовки, не чаще раза в 0,4 с.
{
  let t = 0;
  new MutationObserver(() => {
    clearTimeout(t);
    t = setTimeout(() => markOwned(document.getElementById('view')), 400);
  }).observe(document.getElementById('view'), { childList: true, subtree: true });
}
initNav();
// Любой фокус (стрелкой, экраном напрямую) подводит экран к элементу — здесь одном месте (nav.reveal). Меню
// неподвижно. В приложении для ТВ — без плавности: на слабом телевизоре плавная прокрутка тормозит.
// keyNav — фокус двигают стрелки (пульт, клавиатура). Только тогда страница едет за фокусом: после касания тега
// фокус оставался на нём, и каждая подгрузка подборки, возвращая фокус, отбрасывала страницу к тегам (2026-11-08).
let keyNav = false;
window.addEventListener('keydown', () => { keyNav = true; }, { capture: true, passive: true });
for (const t of ['pointerdown', 'touchstart', 'wheel']) window.addEventListener(t, () => { keyNav = false; }, { capture: true, passive: true });
window.addEventListener('focusin', (e) => {
  const el = e.target;
  if (!keyNav || !el || !el.getBoundingClientRect || el.closest('#top')) return;
  requestAnimationFrame(() => reveal(el, !isClient()));
}, { passive: true });
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
