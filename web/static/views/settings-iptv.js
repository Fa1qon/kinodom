// «Настройки → Каналы» (спека этапа 8, раздел 6.2): плейлисты (ссылкой и файлом, «ограничено»,
// «Обновить», «Проверить», корзина), телепрограмма, ход проверок, часовой пояс каналов, скрытие
// категорий, стран и языков, скрытые поштучно каналы, избранное этого устройства.
import { h, fill, icon, poll, keepFocus, ago, plural } from '../ui.js';
import { get, put, post, del } from '../api.js';
import { layout, remoteNote } from './settings-layout.js';
import { CATEGORIES } from './channel.js';

// fileBase64 — файл плейлиста в base64: изменяющие запросы к API — только JSON.
function fileBase64(file) {
  return new Promise((resolve, reject) => {
    const rd = new FileReader();
    rd.onload = () => resolve(String(rd.result).replace(/^data:[^,]*,/, ''));
    rd.onerror = () => reject(new Error('файл не читается'));
    rd.readAsDataURL(file);
  });
}

// progressLine — «лёгкая: 1200 из 19 000» или «последняя — 2 ч назад».
export function progressLine(name, p) {
  if (p.running) return `${name}: ${p.done} из ${p.total}`;
  return p.finished ? `${name}: ${ago(p.finished)}` : `${name}: ещё не было`;
}

export function render(root, r, ctx) {
  const content = layout(root, 'iptv', 'Каналы');
  let alive = true;
  let pls = null; // /iptv/playlists
  let settings = null; // /settings
  let all = null; // /channels?all=1 — метки и скрытые
  let mine = null; // /channels — избранное устройства
  let error = '';
  let adding = false;
  let confirm = 0; // плейлист, у которого корзина просит подтверждения
  let hidden = null; // черновик скрытия: {categories, countries, languages, otherZones}
  let favKeys = null; // /iptv/favorites — избранное этого устройства как сохранено
  // Поля ввода создаются один раз: опрос перерисовывает экран, а набранное не должно пропадать.
  const url = h('input', { class: 'input', name: 'url', placeholder: 'https://…/playlist.m3u', 'aria-label': 'Ссылка на плейлист', 'data-key': 'pl-url' });
  const limited = h('input', { type: 'checkbox', name: 'limited', 'data-key': 'pl-limited' });
  const epgUrl = h('input', { class: 'input', name: 'epg', 'aria-label': 'Ссылка на телепрограмму', 'data-key': 'epg-url' });
  let epgFilled = false;

  const listPoll = poll(async () => {
    try {
      [pls, settings, favKeys] = await Promise.all([get('/iptv/playlists'), get('/settings'), get('/iptv/favorites').then((r) => r.keys)]);
      if (!all) [all, mine] = await Promise.all([get('/channels?all=1'), get('/channels')]);
      error = '';
    } catch (e) {
      error = e.message;
    }
    if (alive) draw();
  }, 5000);

  async function reloadChannels() {
    try {
      [all, mine] = await Promise.all([get('/channels?all=1'), get('/channels')]);
    } catch (e) {
      error = e.message;
    }
  }

  async function act(fn) {
    try {
      await fn();
      error = '';
    } catch (e) {
      error = e.message;
    }
    listPoll.now();
  }

  const canEdit = () => ctx.canEdit;

  function draw() {
    if (!pls || !settings) {
      fill(content, error ? h('p', { class: 'error' }, error) : h('p', { class: 'muted' }, 'Загружается…'));
      return;
    }
    keepFocus(root, () => fill(content, 
      canEdit() ? null : remoteNote(),
      error ? h('p', { class: 'error' }, error) : null,
      playlistsCard(), epgCard(), hideCard(), hiddenChannelsCard(), favoritesCard()));
  }

  function playlistsCard() {
    const rows = pls.items.map((p) => {
      const conf = confirm === p.id;
      return h('div', { class: 'pl' },
        h('div', { class: 'pl-name' }, h('span', { class: 'strong ellipsis' }, p.name),
          h('span', { class: 'muted small ellipsis', title: p.url }, p.url || 'файл'),
          h('span', { class: 'muted small' }, [plural(p.entries, 'поток', 'потока', 'потоков'), `распознано ${p.recognized}`, `работают ${p.alive}`,
            p.unsupported ? `не поддерживается ${p.unsupported}` : null, p.updatedAt ? `обновлён ${ago(p.updatedAt)}` : null].filter(Boolean).join(' · ')),
          p.error ? h('span', { class: 'error' }, p.error) : null),
        h('label', { class: 'check' }, h('input', { type: 'checkbox', checked: p.limited, disabled: !canEdit(), 'data-key': `limited-${p.id}`,
          onchange: (e) => act(() => put(`/iptv/playlists/${p.id}`, { limited: e.target.checked })) }), 'ограничено'),
        p.url ? h('button', { class: 'sq', type: 'button', 'aria-label': 'Обновить', title: 'Обновить', 'data-key': `refresh-${p.id}`,
          onclick: () => act(() => post(`/iptv/playlists/${p.id}/refresh`)) }, icon('refresh')) : null,
        h('button', { class: 'sq', type: 'button', 'aria-label': 'Проверить', title: 'Проверить', 'data-key': `probe-${p.id}`,
          onclick: () => act(() => post('/iptv/probe', { playlist: p.id })) }, icon('network_check')),
        canEdit() ? h('button', { class: conf ? 'btn danger' : 'sq', type: 'button', 'aria-label': conf ? 'Удалить?' : 'Удалить плейлист', 'data-key': `del-${p.id}`,
          onclick: () => {
            if (!conf) {
              confirm = p.id;
              draw();
              setTimeout(() => {
                if (confirm === p.id) {
                  confirm = 0;
                  if (alive) draw();
                }
              }, 3000);
              return;
            }
            confirm = 0;
            act(async () => {
              await del(`/iptv/playlists/${p.id}`);
              all = null;
            });
          } }, icon('delete'), conf ? 'Удалить?' : null) : null);
    });
    url.disabled = !canEdit() || adding;
    limited.disabled = !canEdit();
    const file = h('input', { type: 'file', accept: '.m3u,.m3u8,audio/x-mpegurl,text/plain', class: 'visually-hidden', 'data-key': 'pl-file-input',
      onchange: () => file.files[0] && add({ name: file.files[0].name.replace(/\.[^.]+$/, ''), data: null, file: file.files[0] }) });
    const add = (src) => act(async () => {
      adding = true;
      draw();
      try {
        const body = { limited: limited.checked };
        if (src.file) {
          body.name = src.name;
          body.data = await fileBase64(src.file);
        } else {
          body.url = url.value.trim();
        }
        await post('/iptv/playlists', body);
        url.value = '';
        all = null;
      } finally {
        adding = false;
      }
    });
    return h('div', { class: 'card' }, h('div', { class: 'h' }, 'Плейлисты'),
      pls.items.length ? h('div', { class: 'pl-list' }, rows) : h('p', { class: 'muted' }, 'Плейлистов нет'),
      canEdit() ? h('form', { class: 'row wrap gap10', onsubmit: (e) => {
        e.preventDefault();
        if (url.value.trim()) add({});
      } }, h('div', { class: 'grow' }, url),
      h('button', { class: 'btn inv', type: 'submit', disabled: adding, 'data-key': 'pl-add' }, icon('add'), adding ? 'Добавляется…' : 'Добавить'),
      h('button', { class: 'btn', type: 'button', disabled: adding, 'data-key': 'pl-file', onclick: () => file.click() }, icon('upload'), 'Файлом'),
      h('label', { class: 'check' }, limited, 'ограничено'), file) : null);
  }

  function epgCard() {
    const e = pls.epg;
    if (!epgFilled) {
      epgUrl.value = settings.iptv.epgUrl;
      epgFilled = true;
    }
    epgUrl.placeholder = e.url;
    epgUrl.disabled = !canEdit();
    const zones = [];
    for (let z = -12; z <= 14; z++) zones.push(h('option', { value: String(z), selected: z === settings.iptv.utcOffset }, `UTC${z >= 0 ? '+' : '−'}${Math.abs(z)}`));
    const zone = h('select', { class: 'input sel', name: 'utc', disabled: !canEdit(), 'aria-label': 'Часовой пояс каналов', 'data-key': 'utc',
      onchange: () => act(() => put('/settings', { iptv: { utcOffset: Number(zone.value) } })) }, zones);
    return h('div', { class: 'card' }, h('div', { class: 'h' }, 'Телепрограмма и проверки'),
      h('label', { class: 'fld' }, 'Телепрограмма', h('div', { class: 'row gap10' }, h('div', { class: 'grow' }, epgUrl),
        canEdit() ? h('button', { class: 'btn', type: 'button', 'data-key': 'epg-save', onclick: () => act(() => put('/settings', { iptv: { epgUrl: epgUrl.value.trim() } })) },
          icon('save'), 'Сохранить') : null)),
      h('div', { class: 'muted small' }, [e.updatedAt ? `обновлена ${ago(e.updatedAt)}` : 'ещё не скачана', e.channels ? plural(e.channels, 'канал', 'канала', 'каналов') : null,
        pls.iptvorg.updatedAt ? `база iptv-org — ${ago(pls.iptvorg.updatedAt)}` : 'база iptv-org ещё не скачана'].filter(Boolean).join(' · ')),
      e.error ? h('div', { class: 'error' }, e.error) : null,
      pls.iptvorg.error ? h('div', { class: 'error' }, pls.iptvorg.error) : null,
      h('div', { class: 'muted small' }, progressLine('Лёгкая проверка', pls.light) + ' · ' + progressLine('полная', pls.full)),
      h('label', { class: 'fld' }, 'Часовой пояс каналов', zone));
  }

  function hideCard() {
    if (!all) return h('div', { class: 'card' }, h('div', { class: 'h' }, 'Скрытие'), h('p', { class: 'muted' }, 'Загружается…'));
    const iv = settings.iptv;
    if (!hidden) hidden = { categories: [...iv.hiddenCategories], countries: [...iv.hiddenCountries], languages: [...iv.hiddenLanguages], otherZones: iv.hideOtherZones };
    const counts = (list) => new Map(list.map((x) => [x.id, x.count]));
    const catCount = counts(all.categories);
    const box = (kind, id, name, n) => h('label', { class: 'check' },
      h('input', { type: 'checkbox', checked: hidden[kind].includes(id), disabled: !canEdit(), 'data-key': `hide-${kind}-${id || 'none'}`, onchange: (e) => {
        hidden[kind] = e.target.checked ? [...hidden[kind], id] : hidden[kind].filter((x) => x !== id);
      } }), n === undefined ? name : `${name} · ${n}`);
    const group = (title, items) => h('div', { class: 'hide-group' }, h('div', { class: 'strong' }, title), h('div', { class: 'hide-list' }, items));
    const dirty = JSON.stringify(hidden) !== JSON.stringify({ categories: iv.hiddenCategories, countries: iv.hiddenCountries, languages: iv.hiddenLanguages, otherZones: iv.hideOtherZones });
    return h('div', { class: 'card' }, h('div', { class: 'row' }, h('div', { class: 'h grow' }, 'Скрытие'),
      canEdit() ? h('button', { class: 'btn inv', type: 'button', disabled: !dirty, 'data-key': 'hide-save', onclick: () => act(async () => {
        await put('/settings', { iptv: { hiddenCategories: hidden.categories, hiddenCountries: hidden.countries, hiddenLanguages: hidden.languages, hideOtherZones: hidden.otherZones } });
        hidden = null;
        await reloadChannels();
      }) }, icon('save'), 'Сохранить') : null),
      group('Категории', CATEGORIES.map(([id, name]) => box('categories', id, name, catCount.get(id)))),
      group('Страны', all.countries.map((x) => box('countries', x.id, x.name, x.count))),
      group('Языки', all.languages.map((x) => box('languages', x.id, x.name, x.count))),
      h('label', { class: 'check' }, h('input', { type: 'checkbox', checked: hidden.otherZones, disabled: !canEdit(), 'data-key': 'hide-zones',
        onchange: (e) => {
          hidden.otherZones = e.target.checked;
        } }), 'Другие часовые пояса'));
  }

  function hiddenChannelsCard() {
    if (!all) return null;
    const list = all.channels.filter((c) => c.hidden === 'channel');
    if (!list.length) return null;
    return h('div', { class: 'card' }, h('div', { class: 'h' }, 'Скрытые каналы'),
      list.map((c) => h('div', { class: 'row' }, h('a', { class: 'grow', href: `#/channel/${encodeURIComponent(c.key)}` }, c.name),
        canEdit() ? h('button', { class: 'btn', type: 'button', 'data-key': `unhide-${c.key}`, onclick: () => act(async () => {
          await put(`/channels/${encodeURIComponent(c.key)}`, { hidden: false });
          await reloadChannels();
        }) }, icon('visibility'), 'Вернуть') : null)));
  }

  // favoritesCard — избранное этого устройства как сохранено (и каналы без живых источников); порядок
  // меняется всем списком, прочитанным с сервера: не прочитался — кнопок нет.
  function favoritesCard() {
    if (!favKeys || !favKeys.length) return null;
    const names = new Map([...(all ? all.channels : []), ...(mine ? mine.channels : [])].map((c) => [c.key, c.name]));
    const keys = favKeys;
    const favs = keys.map((key) => ({ key, name: names.get(key) || key }));
    const save = (next) => act(async () => {
      await put('/iptv/favorites', { keys: next });
      await reloadChannels();
    });
    const move = (i, d) => {
      const next = [...keys];
      [next[i], next[i + d]] = [next[i + d], next[i]];
      save(next);
    };
    return h('div', { class: 'card' }, h('div', { class: 'h' }, 'Избранное этого устройства'),
      favs.map((c, i) => h('div', { class: 'row gap10' }, h('span', { class: 'grow' }, c.name),
        canEdit() ? [
          h('button', { class: 'sq', type: 'button', disabled: i === 0, 'aria-label': 'Выше', 'data-key': `up-${c.key}`, onclick: () => move(i, -1) }, icon('arrow_upward')),
          h('button', { class: 'sq', type: 'button', disabled: i === favs.length - 1, 'aria-label': 'Ниже', 'data-key': `down-${c.key}`, onclick: () => move(i, 1) }, icon('arrow_downward')),
          h('button', { class: 'sq', type: 'button', 'aria-label': 'Убрать из избранного', 'data-key': `unfav-${c.key}`, onclick: () => save(keys.filter((k) => k !== c.key)) }, icon('close')),
        ] : null)));
  }

  return () => {
    alive = false;
    listPoll.stop();
  };
}
