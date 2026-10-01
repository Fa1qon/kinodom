// «Настройки → Медиатека» (спека этапа 9, раздел 6.3): категории по порядку — название, устройство,
// Кинопоиск, скрытая; папки с отметкой «не найдена / не читается»; добавить и убрать папку, добавить и
// удалить свою категорию; у скрытой — «Показывать на этом устройстве». Правки — из домашней сети.
import { h, fill, icon, keepFocus } from '../ui.js';
import { get, put, post, del } from '../api.js';
import { layout, remoteNote } from './settings-layout.js';
import { pickFolder, grantControl } from './folders.js';

const LAYOUTS = [['films', 'Как фильмы — файл или папка = фильм'], ['series', 'Как сериалы — папка = сериал, курс']];
const PROBLEM = { not_found: 'папка не найдена', no_access: 'папка не читается — нет прав', no_write: 'нет права записи' };

// stillDenied — у какой-то папки нет доступа службы: после «Разрешить доступ» категории перечитываются,
// пока это так (хвост Х41).
export function stillDenied(cats) {
  return !!cats && cats.some((c) => (c.folders || []).some((f) => f.problem === 'no_access' || f.problem === 'no_write'));
}

const GRANT_EVERY = 2000; // после «Разрешить доступ» — раз в 2 с…
const GRANT_FOR = 120000; // …не дольше 2 минут: окно Windows «Да/Нет» и обход займут время

export function render(root, r, ctx) {
  const content = layout(root, 'library', 'Медиатека');
  let alive = true;
  let cats = null;
  let error = ''; // список не загрузился
  let failed = ''; // последнее действие не удалось — видно до следующего действия
  let confirm = 0;
  const inputs = new Map(); // поле «папка» по категории: создаётся один раз
  const newName = h('input', { class: 'input', name: 'name', placeholder: 'Название', 'aria-label': 'Название новой категории', 'data-key': 'new-name' });
  const newLayout = h('select', { class: 'input', name: 'layout', 'aria-label': 'Устройство новой категории', 'data-key': 'new-layout' },
    LAYOUTS.map(([v, t]) => h('option', { value: v, selected: v === 'series' }, t)));

  async function load() {
    try {
      cats = await get('/library/categories');
      error = '';
    } catch (e) {
      error = e.message;
    }
    if (alive) draw();
  }
  load();

  async function act(fn) {
    try {
      await fn();
      failed = '';
    } catch (e) {
      failed = e.message;
    }
    await load();
  }

  // watchGrant — нажали «Разрешить доступ»: итог окна Windows придёт позже — перечитывать категории.
  let grantTimer = 0;
  function watchGrant() {
    const until = Date.now() + GRANT_FOR;
    clearInterval(grantTimer);
    grantTimer = setInterval(async () => {
      if (!alive || Date.now() > until) {
        clearInterval(grantTimer);
        return;
      }
      await load();
      if (!stillDenied(cats)) clearInterval(grantTimer);
    }, GRANT_EVERY);
  }

  const canEdit = () => ctx.canEdit;
  const input = (c) => ({ name: c.name, layout: c.layout, kinopoisk: c.kinopoisk, hidden: c.hidden, folders: c.folders.map((f) => f.path) });
  const save = (c, patch) => act(() => put(`/library/categories/${c.id}`, { ...input(c), ...patch }));

  function folderInput(c) {
    let el = inputs.get(c.id);
    if (!el) {
      el = h('input', { class: 'input', name: 'folder', placeholder: 'Полный путь, например D:\\Share\\Movies', 'aria-label': `Папка для «${c.name}»`, 'data-key': `folder-${c.id}` });
      inputs.set(c.id, el);
    }
    el.disabled = !canEdit();
    return el;
  }

  function draw() {
    if (!cats) {
      fill(content, error ? h('p', { class: 'error' }, error) : h('p', { class: 'muted' }, 'Загружается…'));
      return;
    }
    keepFocus(root, () => fill(content, canEdit() ? null : remoteNote(), error || failed ? h('p', { class: 'error' }, failed || error) : null,
      cats.map(category), canEdit() ? addCard() : null));
  }

  function category(c) {
    const own = !c.builtin;
    const conf = confirm === c.id;
    const fin = folderInput(c);
    return h('section', { class: 'card', 'aria-label': c.name },
      h('div', { class: 'row' }, h('div', { class: 'h grow' }, c.name),
        own && canEdit() ? h('button', { class: conf ? 'btn danger' : 'sq', type: 'button', 'data-key': `del-${c.id}`, 'aria-label': conf ? 'Удалить?' : 'Удалить категорию',
          onclick: () => {
            if (!conf) {
              confirm = c.id;
              draw();
              setTimeout(() => {
                if (confirm === c.id) {
                  confirm = 0;
                  if (alive) draw();
                }
              }, 3000);
              return;
            }
            confirm = 0;
            act(() => del(`/library/categories/${c.id}`));
          } }, icon('delete'), conf ? 'Удалить?' : null) : null),
      own ? h('div', { class: 'row wrap gap10' },
        h('select', { class: 'input sel', name: 'layout', 'aria-label': 'Устройство', disabled: !canEdit(), 'data-key': `layout-${c.id}`,
          onchange: (e) => save(c, { layout: e.target.value }) }, LAYOUTS.map(([v, t]) => h('option', { value: v, selected: v === c.layout }, t))),
        h('label', { class: 'check' }, h('input', { type: 'checkbox', checked: c.kinopoisk, disabled: !canEdit(), 'data-key': `kp-${c.id}`,
          onchange: (e) => save(c, { kinopoisk: e.target.checked }) }), 'Кинопоиск'),
        h('label', { class: 'check' }, h('input', { type: 'checkbox', checked: c.hidden, disabled: !canEdit(), 'data-key': `hidden-${c.id}`,
          onchange: (e) => save(c, { hidden: e.target.checked }) }), 'Скрытая')) : null,
      c.hidden ? h('label', { class: 'check' }, h('input', { type: 'checkbox', checked: c.onDevice, disabled: !canEdit(), 'data-key': `device-${c.id}`,
        onchange: (e) => act(() => (e.target.checked ? put : del)(`/library/categories/${c.id}/device`)) }), 'Показывать на этом устройстве') : null,
      h('div', { class: 'list' }, c.folders.map((f) => h('div', { class: 'row' },
        icon('folder'), h('span', { class: 'grow ellipsis', title: f.path }, f.path),
        f.problem ? h('span', { class: 'tag warn-tag' }, icon('warning', 16), PROBLEM[f.problem] || f.problem) : null,
        canEdit() ? grantControl(ctx, f, `grant-${f.id}`, watchGrant) : null,
        canEdit() ? h('button', { class: 'sq', type: 'button', 'aria-label': `Убрать папку ${f.path}`, 'data-key': `unfolder-${f.id}`,
          onclick: () => save(c, { folders: c.folders.filter((x) => x.id !== f.id).map((x) => x.path) }) }, icon('close')) : null))),
      canEdit() ? h('form', { class: 'row gap10', onsubmit: (e) => {
        e.preventDefault();
        const p = fin.value.trim();
        if (!p) return;
        act(async () => {
          await put(`/library/categories/${c.id}`, { ...input(c), folders: [...c.folders.map((x) => x.path), p] });
          fin.value = '';
        });
      } }, fin,
        h('button', { class: 'btn', type: 'button', 'data-key': `browse-${c.id}`, onclick: async () => {
          const p = await pickFolder(fin.value.trim());
          if (p) act(() => put(`/library/categories/${c.id}`, { ...input(c), folders: [...c.folders.map((x) => x.path), p] }));
        } }, icon('folder_open'), 'Обзор'),
        h('button', { class: 'btn', type: 'submit', 'data-key': `addfolder-${c.id}` }, icon('add'), 'Добавить')) : null);
  }

  function addCard() {
    return h('section', { class: 'card', 'aria-label': 'Новая категория' }, h('div', { class: 'h' }, 'Новая категория'),
      h('form', { class: 'row wrap gap10', onsubmit: (e) => {
        e.preventDefault();
        act(async () => {
          await post('/library/categories', { name: newName.value, layout: newLayout.value, kinopoisk: false, hidden: false, folders: [] });
          newName.value = '';
        });
      } }, newName, newLayout, h('button', { class: 'btn', type: 'submit', 'data-key': 'add-cat' }, icon('add'), 'Добавить')));
  }

  const onStatus = () => {
    if (alive && cats) draw();
  };
  ctx.listeners.add(onStatus); // canEdit пришёл позже — кнопки правки появляются
  return () => {
    alive = false;
    clearInterval(grantTimer);
    ctx.listeners.delete(onStatus);
  };
}
