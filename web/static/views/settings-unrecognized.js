// «Настройки → Каналы → Не распознано» (спека этапа 8, разделы 5.3 и 6.2; этап 11b, замечание № 5):
// потоки, которые не привязались ни к одному каналу, — по названию; «Назначить» — поиск канала
// телепрограммы; «Скрыть»; скрытые названия — внизу, с «Вернуть» (хвост Х30). Страницы по 50.
import { h, fill, icon, keepFocus, plural } from '../ui.js';
import { get, put, post } from '../api.js';
import { layout, remoteNote, channelTabs } from './settings-layout.js';
import { openPreview } from './preview.js';
import { CATEGORIES, pickLabel } from './channel-settings.js';
import { appBridge } from './tvkit.js';

// fileFieldShown — поле выбора файла типа type («image/*» — логотип, «*/*» — плейлист): в браузере — да; в
// приложении — если оно умеет выбирать файлы и на устройстве есть чем (на ТВ часто нечем; старое приложение не
// умеет) (ревью 14Д, п. 12; 15Г, п. 8).
export function fileFieldShown(app, type = 'image/*') {
  if (!app) return true;
  try {
    return typeof app.canPickFiles === 'function' && !!app.canPickFiles(type);
  } catch {
    return false;
  }
}

// logoFileError — файл логотипа больше 1 МБ: отказ до отправки (фото с телефона; ревью 14Д, п. 10).
export function logoFileError(f) {
  return f && f.size > 1 << 20 ? 'логотип больше 1 МБ' : '';
}

export function render(root, r, ctx) {
  const q = r.query.get('q') || '';
  const page = Math.max(1, Number(r.query.get('page')) || 1);
  const content = layout(root, 'iptv', 'Каналы');
  content.append(channelTabs('unrecognized'));
  let hiddenNames = []; // /iptv/names?hidden=1
  let alive = true;
  let data = null;
  let error = '';
  let open = ''; // название, для которого открыт поиск канала
  let found = [];
  let making = ''; // название, для которого открыта форма «Новый канал» (план 14Д)
  let saving = false; // «Создать канал» отправлен — второе нажатие не создаст второй канал
  let formError = ''; // ошибка формы «Новый канал» — у формы, а не над списком
  const nName = h('input', { class: 'input', name: 'name', placeholder: 'Название', 'aria-label': 'Название', 'data-key': 'new-name' });
  const nLogo = h('input', { class: 'input', name: 'logo', placeholder: 'Адрес логотипа', 'aria-label': 'Адрес логотипа', inputmode: 'url', 'data-key': 'new-logo' });
  const nFile = h('input', { type: 'file', accept: 'image/*', 'aria-label': 'Файл логотипа', 'data-key': 'new-file' });
  const nCat = h('select', { class: 'input', name: 'category', 'aria-label': 'Категория', 'data-key': 'new-category' },
    h('option', { value: '' }, 'Без категории'), CATEGORIES.filter(([v]) => v).map(([v, t]) => h('option', { value: v }, t)));
  const nCountry = h('input', { class: 'input', name: 'country', placeholder: 'Страна: RU', 'aria-label': 'Страна', maxlength: '2', 'data-key': 'new-country' });

  // fileBase64 — выбранный файл логотипа в base64 (без «data:…,»); нет файла — "".
  function fileBase64() {
    const f = nFile.files && nFile.files[0];
    if (!f) return Promise.resolve('');
    return new Promise((resolve, reject) => {
      const r = new FileReader();
      r.onload = () => resolve(String(r.result).replace(/^data:[^,]*,/, ''));
      r.onerror = () => reject(new Error('файл не прочитался'));
      r.readAsDataURL(f);
    });
  }

  const search = h('input', { class: 'input', name: 'q', value: q, placeholder: 'Название', 'aria-label': 'Название', 'data-key': 'q' });
  const pick = h('input', { class: 'input', name: 'channel', placeholder: 'Канал в телепрограмме', 'aria-label': 'Канал в телепрограмме', 'data-key': 'pick-q' });
  const list = h('div', { class: 'un-list' });
  const pages = h('div', { class: 'pages' });
  const hiddenBox = h('section', { class: 'un-hidden', 'aria-label': 'Скрытые' });
  content.append(
    h('form', { class: 'row gap10', onsubmit: (e) => {
      e.preventDefault();
      ctx.go('#/settings/iptv/unrecognized' + (search.value.trim() ? '?q=' + encodeURIComponent(search.value.trim()) : ''));
    } }, h('div', { class: 'grow' }, search), h('button', { class: 'btn', type: 'submit', 'data-key': 'find' }, icon('search'), 'Найти')),
    list, pages, hiddenBox);

  async function load() {
    try {
      [data, hiddenNames] = await Promise.all([get(`/iptv/unrecognized?q=${encodeURIComponent(q)}&page=${page}`),
        get('/iptv/names?hidden=1').then((v) => v.items)]);
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
      open = '';
      error = '';
    } catch (e) {
      error = e.message;
    }
    load();
  }

  function draw() {
    if (!data) {
      fill(list, error ? h('p', { class: 'error' }, error) : h('p', { class: 'muted' }, 'Загружается…'));
      return;
    }
    keepFocus(root, () => {
      const rows = data.items.map((u) => {
        const row = h('div', { class: 'un' },
          h('div', { class: 'row un-top' },
            h('div', { class: 'grow un-name' }, h('span', { class: 'strong' }, u.sample),
              h('span', { class: 'muted small' }, [plural(u.streams, 'поток', 'потока', 'потоков'), `работают ${u.alive}`, u.playlists.join(', ')].join(' · '))),
            h('div', { class: 'row wrap gap10 un-acts' },
            ctx.canEdit && u.watch ? h('button', { class: 'btn', type: 'button', 'data-key': `watch-${u.name}`,
              onclick: () => openPreview({ id: u.watch, url: u.watchUrl, kind: u.watchKind }, u.sample) }, icon('play_arrow'), 'Смотреть') : null,
            ctx.canEdit ? h('button', { class: 'btn', type: 'button', 'data-key': `new-${u.name}`, onclick: () => {
              making = making === u.name ? '' : u.name;
              open = '';
              formError = '';
              nName.value = u.sample;
              nLogo.value = '';
              nFile.value = '';
              nCat.value = '';
              nCountry.value = '';
              draw();
              if (making) nName.focus();
            } }, icon('add'), 'Новый канал') : null,
            ctx.canEdit ? h('button', { class: 'btn', type: 'button', 'data-key': `assign-${u.name}`, onclick: () => {
              open = open === u.name ? '' : u.name;
              found = [];
              pick.value = u.sample;
              draw();
              if (open) pick.focus();
            } }, icon('swap_horiz'), 'Назначить') : null,
            ctx.canEdit ? h('button', { class: 'btn', type: 'button', 'data-key': `hide-${u.name}`, onclick: () => act(() => put('/iptv/names', { name: u.name, hidden: true })) },
              icon('visibility_off'), 'Скрыть') : null)));
        if (open === u.name) {
          row.append(h('form', { class: 'row gap10', onsubmit: async (e) => {
            e.preventDefault();
            try {
              found = (await get(`/iptv/epg-channels?q=${encodeURIComponent(pick.value)}`)).items;
            } catch (err) {
              error = err.message;
            }
            draw();
            const first = list.querySelector('[data-key^="to-"]');
            if (first) first.focus();
          } }, h('div', { class: 'grow' }, pick), h('button', { class: 'btn', type: 'submit', 'data-key': 'pick-find' }, icon('search'), 'Найти')),
          h('div', { class: 'picks' }, found.map((c) => h('button', { class: 'btn', type: 'button', 'data-key': `to-${c.key}`,
            onclick: () => act(() => put('/iptv/names', { name: u.name, channel: c.key })) }, pickLabel(c)))));
        }
        if (making === u.name) {
          row.append(h('form', { class: 'new-channel col gap10', onsubmit: async (e) => {
            e.preventDefault();
            if (saving) return;
            formError = logoFileError(nFile.files && nFile.files[0]);
            if (formError) return draw();
            saving = true;
            draw();
            try {
              const logoData = await fileBase64();
              const { key } = await post('/iptv/custom', { name: nName.value, logo: logoData ? '' : nLogo.value.trim(), logoData: logoData || null,
                category: nCat.value, country: nCountry.value.trim().toUpperCase(), group: u.name });
              making = '';
              ctx.go(`#/channel/${encodeURIComponent(key)}/settings`); // сразу видно, что вышло; там же «Удалить канал»
            } catch (err) {
              formError = err.message;
            } finally {
              saving = false;
              if (alive) draw();
            }
          } }, h('div', { class: 'row gap10 wrap' }, h('div', { class: 'grow' }, nName), nCat, nCountry),
          h('div', { class: 'row gap10 wrap' }, h('div', { class: 'grow' }, nLogo), fileFieldShown(appBridge()) ? nFile : null),
          formError ? h('p', { class: 'error' }, formError) : null,
          h('div', { class: 'row gap10' }, h('button', { class: 'btn inv', type: 'submit', disabled: saving, 'data-key': 'new-save' },
            icon('save'), saving ? 'Создаётся…' : 'Создать канал'))));
        }
        return row;
      });
      fill(list, ctx.canEdit ? '' : remoteNote(), error ? h('p', { class: 'error' }, error) : '',
        h('div', { class: 'muted' }, plural(data.total, 'название', 'названия', 'названий')),
        ...(rows.length ? rows : [h('p', { class: 'empty' }, 'Всё распознано')]));
      const link = (n) => `#/settings/iptv/unrecognized?${q ? 'q=' + encodeURIComponent(q) + '&' : ''}page=${n}`;
      fill(pages, ...(data.pages > 1 ? [
        page > 1 ? h('a', { class: 'page', href: link(page - 1), 'aria-label': 'Назад', 'data-key': 'prev' }, icon('chevron_left')) : null,
        h('span', { class: 'page on' }, String(page)),
        h('span', { class: 'muted' }, `из ${data.pages}`),
        page < data.pages ? h('a', { class: 'page', href: link(page + 1), 'aria-label': 'Вперёд', 'data-key': 'next' }, icon('chevron_right')) : null,
      ] : []));
      fill(hiddenBox, ...(hiddenNames.length ? [h('h2', null, 'Скрытые'), h('div', { class: 'un-list' }, hiddenNames.map((n) =>
        h('div', { class: 'row un' }, h('span', { class: 'grow' }, n.name),
          ctx.canEdit ? h('button', { class: 'btn', type: 'button', 'data-key': `unhide-${n.name}`,
            onclick: () => act(() => put('/iptv/names', { name: n.name, hidden: false })) }, icon('visibility'), 'Вернуть') : null)))] : []));
    });
  }

  const onStatus = () => {
    if (alive && data) draw();
  };
  ctx.listeners.add(onStatus); // canEdit пришёл позже списка — кнопки появляются
  return () => {
    alive = false;
    ctx.listeners.delete(onStatus);
  };
}
