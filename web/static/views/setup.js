// Мастер начальных настроек (спека этапа 11a, раздел 7): раздачи, каналы, медиатека, «Готово» (шага
// «Кинопоиск» нет — он работает без ключа, спека 11b, 5.7). Открывается сам после установки, пока не пройден; снова — «Параметры → Открыть мастер».
// У каждого шага «Назад», «Пропустить», «Далее»; поля заполнены текущими настройками.
import { h, fill, icon, poll, keepFocus, fileBase64 } from '../ui.js';
import { get, put, post } from '../api.js';
import { remoteNote } from './settings-layout.js';
import { pickFolder, grantControl } from './folders.js';

export const STEPS = [
  { id: 'trackers', title: 'Раздачи' },
  { id: 'channels', title: 'Каналы' },
  { id: 'library', title: 'Медиатека' },
  { id: 'done', title: 'Готово' },
];

export const nextStep = (i) => Math.min(i + 1, STEPS.length - 1);
export const prevStep = (i) => Math.max(i - 1, 0);

// shouldOpenSetup — открыть мастер вместо каталога: из домашней сети, пока мастер не пройден. Ссылка
// на раздачу или настройки ведёт туда, куда вела.
export function shouldOpenSetup(status, hash) {
  if (!status || !status.canEdit || status.setupDone) return false;
  const p = String(hash || '').replace(/^#\/?/, '');
  return p === '' || p === 'catalog' || p.startsWith('catalog/');
}

export function render(root, r, ctx) {
  const idx = Math.max(0, STEPS.findIndex((s) => s.id === r.parts[1]));
  const step = STEPS[idx];
  let alive = true;
  let stop = null; // опрос шага: каналы и медиатека
  let pollFailed = false; // ошибка на экране — от опроса: удачный опрос её снимает
  const pollError = (e) => {
    err.textContent = e ? e.message : pollFailed ? '' : err.textContent;
    pollFailed = !!e;
  };
  const err = h('p', { class: 'error' });
  const body = h('div', { class: 'setup-body' }, h('p', { class: 'muted' }, 'Загружается…'));
  const go = (i) => ctx.go('#/setup/' + STEPS[i].id);
  const canEdit = () => !ctx.status || ctx.canEdit;
  let next = async () => true; // сохранить шаг перед «Далее»; false — остаться
  const nextBtn = h('button', { class: 'btn inv', type: 'button', 'data-key': 'setup-next', onclick: async () => {
    nextBtn.disabled = true;
    err.textContent = '';
    try {
      if (await next()) go(nextStep(idx));
    } catch (e) {
      err.textContent = e.message;
    }
    nextBtn.disabled = false;
  } }, 'Далее', icon('chevron_right'));
  root.append(h('div', { class: 'screen setup' },
    h('h1', null, 'Начальная настройка'),
    h('ol', { class: 'steps' }, STEPS.map((s, i) => h('li', { class: i === idx ? 'on' : i < idx ? 'past' : null, 'aria-current': i === idx ? 'step' : null },
      h('span', { class: 'num' }, String(i + 1)), s.title))),
    ctx.status && !ctx.canEdit ? remoteNote() : null,
    err, body,
    step.id === 'done' ? null : h('div', { class: 'row gap10 setup-foot' },
      idx > 0 ? h('button', { class: 'btn', type: 'button', 'data-key': 'setup-back', onclick: () => go(prevStep(idx)) }, icon('chevron_left'), 'Назад') : null,
      h('div', { class: 'grow' }),
      h('button', { class: 'btn', type: 'button', 'data-key': 'setup-skip', onclick: () => go(nextStep(idx)) }, 'Пропустить'),
      nextBtn)));

  const input = (key, value, attrs = {}) => h('input', { class: 'input', name: key, value: value ?? '', 'data-key': key, disabled: !canEdit(), autocomplete: 'off', ...attrs });
  const secret = (key, isSet) => input(key, '', { type: 'password', placeholder: isSet ? 'задан' : '', autocomplete: 'new-password' });
  const field = (label, control) => h('label', { class: 'fld' }, label, control);
  // result — строка итога «Проверить»: зелёная — всё хорошо, жёлтая — что не так.
  const result = () => h('div', { class: 'small check-result', role: 'status' });
  const showResult = (el, res) => {
    el.textContent = res.text;
    el.classList.toggle('good', res.ok);
    el.classList.toggle('bad', !res.ok);
  };
  const checkBtn = (key, fn) => h('button', { class: 'btn', type: 'button', 'data-key': key, disabled: !canEdit(), onclick: async (e) => {
    const b = e.currentTarget;
    b.disabled = true;
    err.textContent = '';
    try {
      await fn();
    } catch (x) {
      err.textContent = x.message;
    }
    b.disabled = false;
  } }, icon('network_check'), 'Проверить');

  const steps = {
    // Раздачи: адреса и вход Rutracker, адрес Rutor, прокси. «Проверить» сначала сохраняет поля.
    async trackers() {
      const v = await get('/settings');
      const f = {
        rtAddress: input('rtAddress', v.rutracker.address, { inputmode: 'url' }), rtLogin: input('rtLogin', v.rutracker.login),
        rtPassword: secret('rtPassword', v.rutracker.passwordSet), rutorAddress: input('rutorAddress', v.rutor.address, { inputmode: 'url' }),
        proxyAddress: input('proxyAddress', v.proxy.address), proxyLogin: input('proxyLogin', v.proxy.login), proxyPassword: secret('proxyPassword', v.proxy.passwordSet),
      };
      const types = [['none', 'Нет'], ['http', 'HTTP'], ['socks5', 'SOCKS5']];
      const ptype = h('div', { class: 'checks', role: 'radiogroup', 'aria-label': 'Прокси' }, types.map(([id, t]) => h('label', { class: 'check' },
        h('input', { type: 'radio', name: 'ptype', value: id, checked: v.proxy.type === id, disabled: !canEdit(), 'data-key': `ptype-${id}` }), t)));
      const rtResult = result();
      const rutorResult = result();
      const save = async () => {
        const val = (k) => f[k].value.trim();
        const p = { rutracker: { address: val('rtAddress'), login: val('rtLogin') }, rutor: { address: val('rutorAddress') } };
        if (f.rtPassword.value) p.rutracker.password = f.rtPassword.value;
        const type = ptype.querySelector('input:checked').value;
        if (type !== v.proxy.type || val('proxyAddress') !== v.proxy.address || val('proxyLogin') !== v.proxy.login || f.proxyPassword.value) {
          p.proxy = type === 'none' ? { type } : { type, address: val('proxyAddress'), login: val('proxyLogin') };
          if (type !== 'none' && f.proxyPassword.value) p.proxy.password = f.proxyPassword.value;
        }
        Object.assign(v, await put('/settings', p));
        f.rtAddress.value = v.rutracker.address; // адрес — как его сохранил сервер: «схема://хост»
        f.rutorAddress.value = v.rutor.address;
        f.rtPassword.value = f.proxyPassword.value = '';
        f.rtPassword.placeholder = v.rutracker.passwordSet ? 'задан' : '';
        f.proxyPassword.placeholder = v.proxy.passwordSet ? 'задан' : '';
      };
      const check = (tracker, el) => async () => {
        await save();
        el.textContent = 'Проверяю…';
        el.classList.remove('good', 'bad');
        showResult(el, await post('/setup/check', { tracker }));
      };
      next = async () => {
        await save();
        return true;
      };
      return [
        h('div', { class: 'card' }, h('div', { class: 'h' }, 'Rutracker'), field('Адрес сайта', f.rtAddress),
          h('div', { class: 'two' }, field('Логин', f.rtLogin), field('Пароль', f.rtPassword)),
          h('div', { class: 'row gap10' }, checkBtn('check-rutracker', check('rutracker', rtResult)), rtResult)),
        h('div', { class: 'card' }, h('div', { class: 'h' }, 'Rutor'), field('Адрес сайта или зеркала', f.rutorAddress),
          h('div', { class: 'row gap10' }, checkBtn('check-rutor', check('rutor', rutorResult)), rutorResult)),
        h('div', { class: 'card' }, h('div', { class: 'h' }, 'Прокси для трекеров'), ptype, field('Адрес', f.proxyAddress),
          h('div', { class: 'two' }, field('Логин', f.proxyLogin), field('Пароль', f.proxyPassword))),
      ];
    },

    // Каналы: плейлист ссылкой или файлом; потоки и каналы — по мере разбора.
    async channels() {
      const list = h('div', { class: 'list' });
      const url = input('plUrl', '', { inputmode: 'url', placeholder: 'Ссылка на плейлист', 'aria-label': 'Ссылка на плейлист' });
      const file = h('input', { type: 'file', accept: '.m3u,.m3u8,audio/x-mpegurl,text/plain', class: 'visually-hidden', 'data-key': 'pl-file-input',
        onchange: () => file.files[0] && add({ file: file.files[0] }) });
      const draw = (pls) => fill(list, pls.items.length ? pls.items.map((p) => h('div', { class: 'row' }, icon('playlist_play'),
        h('span', { class: 'grow ellipsis' }, p.name), h('span', { class: 'muted small' }, `потоков ${p.entries}, каналов ${p.recognized}`),
        p.error ? h('span', { class: 'error small' }, p.error) : null)) : h('p', { class: 'muted' }, 'Плейлистов нет'));
      const refresh = async () => {
        try {
          const pls = await get('/iptv/playlists');
          pollError(null);
          if (alive) draw(pls);
        } catch (e) {
          pollError(e); // опрос продолжается
        }
      };
      const add = async (src) => {
        err.textContent = '';
        try {
          const b = { limited: false };
          if (src.file) {
            b.name = src.file.name.replace(/\.[^.]+$/, '');
            b.data = await fileBase64(src.file);
          } else {
            b.url = url.value.trim();
          }
          await post('/iptv/playlists', b);
          url.value = '';
          await refresh();
        } catch (e) {
          err.textContent = e.message;
        }
      };
      await refresh();
      stop = poll(refresh, 2000).stop;
      return h('div', { class: 'card' }, h('div', { class: 'h' }, 'Плейлисты каналов'), list,
        canEdit() ? h('form', { class: 'row wrap gap10', onsubmit: (e) => {
          e.preventDefault();
          if (url.value.trim()) add({});
        } }, h('div', { class: 'grow' }, url), h('button', { class: 'btn inv', type: 'submit', 'data-key': 'pl-add' }, icon('add'), 'Добавить'),
        h('button', { class: 'btn', type: 'button', 'data-key': 'pl-file', onclick: () => file.click() }, icon('upload'), 'Файлом'), file) : null);
    },

    // Медиатека: папки с фильмами и сериалами (обзор кликами или путь), «Найти» — обход и сверка с
    // Кинопоиском, ход — «найдено N, распознано M».
    async library() {
      let cats = await get('/library/categories');
      const wanted = [['films', 'Папка с фильмами'], ['series', 'Папка с сериалами']];
      const rows = new Map(); // категория → поля новых папок
      const progress = h('div', { class: 'small', role: 'status' });
      const box = h('div', { class: 'setup-body' });
      // Опрос перерисовывает карточки, только если изменились папки или их проблемы, и не сбивает
      // фокус пульта ТВ (keepFocus по data-key).
      const folderKey = (cs) => JSON.stringify(cs.map((c) => [c.id, c.folders.map((f) => [f.id, f.problem])]));
      let drawn = '';
      let quiet = 0; // опросов подряд без перемен после конца обхода
      // watchAgain — после «Разрешить доступ»: окно Windows «Да/Нет» и обход займут время, опрос снова.
      const watchAgain = () => {
        quiet = 0;
        if (!stop) stop = poll(watch, 2000).stop;
      };
      const draw = () => keepFocus(box, () => {
        drawn = folderKey(cats);
        fill(box, wanted.map(([builtin, label]) => {
          const c = cats.find((x) => x.builtin === builtin);
          if (!c) return null;
          if (!rows.has(c.id)) rows.set(c.id, [input(`new-${c.id}-0`, '', { 'aria-label': label })]);
          const inputs = rows.get(c.id);
          return h('div', { class: 'card' }, h('div', { class: 'h' }, label),
            c.folders.map((f) => h('div', { class: 'row folder-row' }, icon('folder'), h('span', { class: 'grow ellipsis', title: f.path }, f.path),
              f.problem ? h('span', { class: 'tag warn-tag' }, icon('warning', 16), f.problem === 'no_access' ? 'нет доступа' : 'не найдена') : null,
              grantControl(ctx, f, `grant-${f.id}`, watchAgain))),
            inputs.map((el, i) => h('div', { class: 'row gap10' }, h('div', { class: 'grow' }, el),
              h('button', { class: 'btn', type: 'button', 'data-key': `browse-${c.id}-${i}`, disabled: !canEdit(), onclick: async () => {
                const p = await pickFolder(el.value.trim());
                if (p) el.value = p;
              } }, icon('folder_open'), 'Обзор'))),
            h('button', { class: 'link', type: 'button', 'data-key': `more-${c.id}`, disabled: !canEdit(), onclick: () => {
              inputs.push(input(`new-${c.id}-${inputs.length}`, '', { 'aria-label': label }));
              draw();
            } }, 'Ещё папка'));
        }));
      });
      const watch = async () => {
        let lib;
        let cs;
        try {
          [lib, cs] = await Promise.all([get('/library'), get('/library/categories')]);
          pollError(null);
        } catch (e) {
          pollError(e); // опрос продолжается
          return;
        }
        if (!alive) return;
        cats = cs;
        const recognized = lib.categories.reduce((n, c) => n + c.count, 0);
        const text = `найдено ${recognized + lib.unrecognized}, распознано ${recognized}` + (lib.scan.running ? ' — идёт обход' : '');
        // Обход кончился, а числа три опроса подряд не меняются (сверка с Кинопоиском дошла) — хватит.
        quiet = !lib.scan.running && text === progress.textContent ? quiet + 1 : 0;
        progress.textContent = text;
        if (folderKey(cats) !== drawn) draw();
        if (quiet >= 3 && stop) {
          stop();
          stop = null;
        }
      };
      const find = async () => {
        for (const c of cats) {
          const add = (rows.get(c.id) || []).map((el) => el.value.trim()).filter(Boolean);
          if (!add.length) continue;
          await put(`/library/categories/${c.id}`, { name: c.name, layout: c.layout, kinopoisk: c.kinopoisk, hidden: c.hidden,
            folders: [...c.folders.map((f) => f.path), ...add] });
          rows.delete(c.id);
        }
        await post('/library/scan');
        quiet = 0;
        await watch();
        if (!stop) stop = poll(watch, 2000).stop;
      };
      next = async () => {
        if ([...rows.values()].some((l) => l.some((el) => el.value.trim()))) await find();
        return true;
      };
      draw();
      return [box, h('div', { class: 'row gap10' }, h('button', { class: 'btn inv', type: 'button', 'data-key': 'lib-find', disabled: !canEdit(), onclick: async (e) => {
        const b = e.currentTarget;
        b.disabled = true;
        err.textContent = '';
        try {
          await find();
        } catch (x) {
          err.textContent = x.message;
        }
        b.disabled = false;
      } }, icon('search'), 'Найти'), progress)];
    },

    // Готово: адреса для телефонов и ТВ; мастер пройден.
    async done() {
      const [addrs] = await Promise.all([get('/setup/addresses'), canEdit() ? put('/settings', { setup: { done: true } }) : null]);
      ctx.refreshStatus();
      return h('div', { class: 'card' }, h('div', { class: 'h' }, 'Откройте на телефоне и ТВ'),
        // Первый — обычная домашняя сеть (192.168.*): крупно; остальные (VPN, виртуальные сети) — мелко.
        addrs.length ? h('div', { class: 'setup-addr' }, addrs[0]) : h('p', { class: 'muted' }, 'Адрес ПК в домашней сети не найден'),
        addrs.length > 1 ? h('div', { class: 'muted small' }, 'Другие адреса: ' + addrs.slice(1).join(', ')) : null,
        h('div', null, h('a', { class: 'btn inv', href: '#/catalog/rutor', 'data-key': 'to-catalog' }, 'В каталог', icon('chevron_right'))));
    },
  };

  steps[step.id]().then((content) => {
    if (!alive) return;
    fill(body, content);
    const first = body.querySelector('input:not([type="hidden"]):not(.visually-hidden), button, a[href]');
    if (first) first.focus({ preventScroll: true });
  }, (e) => {
    if (alive) fill(body, h('p', { class: 'error' }, e.message));
  });

  return () => {
    alive = false;
    if (stop) stop();
  };
}
