// «Настройки → Приложение» (спека этапа 13, 5.2): только в приложении для Android — плеер каналов (встроенный
// или системный: VLC и другие плееры Android), плеер фильмов (план 18В) и обновление приложения. Настройка хранится
// на устройстве.
import { h, fill, keepFocus } from '../ui.js';
import { get } from '../api.js';
import { layout } from './settings-layout.js';
import { appBridge } from './tvkit.js';
import { appModel, appCard } from './settings-status.js';

const PLAYERS = [['builtin', 'Встроенный'], ['system', 'Системный (VLC и другие)']];
const MOVIE_PLAYERS = [['builtin', 'Встроенный'], ['system', 'VLC']];
const SOUNDS = [['on', 'Включены'], ['off', 'Выключены']];

export function render(root) {
  const app = appBridge();
  if (!app) {
    location.replace('#/settings/status'); // в браузере раздела нет
    return;
  }
  const version = () => h('p', { class: 'muted' }, 'Версия приложения ' + (typeof app.version === 'function' ? app.version() : ''));
  const body = h('div');
  const update = h('div', null, version());
  layout(root, 'app', 'Приложение').append(body, update);
  draw();
  // Версии и «Обновить» вместо строки версии; APK на сервере нет — только строка версии.
  get('/app').then((info) => fill(update, appCard(appModel(info, [], app), app) || version()), () => {});

  function draw() {
    // У приложения 13a настройки плеера нет — только версия.
    const canSet = typeof app.player === 'function' && typeof app.setPlayer === 'function';
    const mode = canSet ? app.player() : '';
    // Плеер фильмов (план 18В) — у приложения, которое его умеет.
    const canMovie = typeof app.moviePlayer === 'function' && typeof app.setMoviePlayer === 'function';
    const movie = canMovie ? app.moviePlayer() : '';
    // Звуки меню (план 16В) — у приложения, которое их умеет.
    const canSound = typeof app.soundsOn === 'function' && typeof app.setSoundsOn === 'function';
    const sound = canSound && app.soundsOn() ? 'on' : 'off';
    // keepFocus — после выбора фокус на том же переключателе (ревью 16В: уходил на body, «вниз» прыгал в меню).
    keepFocus(body, () => fill(body,
      canSet ? h('section', { class: 'card' }, h('div', { class: 'h' }, 'Плеер каналов'),
        h('div', { class: 'seg', role: 'radiogroup', 'aria-label': 'Плеер каналов' }, PLAYERS.map(([id, t]) => h('label', { class: id === mode ? 'on' : null }, t,
          h('input', { type: 'radio', name: 'player', checked: id === mode, 'data-key': `player-${id}`, onchange: () => {
            app.setPlayer(id);
            draw();
          } }))))) : null,
      canMovie ? h('section', { class: 'card' }, h('div', { class: 'h' }, 'Плеер фильмов'),
        h('div', { class: 'seg', role: 'radiogroup', 'aria-label': 'Плеер фильмов' }, MOVIE_PLAYERS.map(([id, t]) => h('label', { class: id === movie ? 'on' : null }, t,
          h('input', { type: 'radio', name: 'movie-player', checked: id === movie, 'data-key': `movie-${id}`, onchange: () => {
            app.setMoviePlayer(id);
            draw();
          } }))))) : null,
      canSound ? h('section', { class: 'card' }, h('div', { class: 'h' }, 'Звуки меню'),
        h('div', { class: 'seg', role: 'radiogroup', 'aria-label': 'Звуки меню' }, SOUNDS.map(([id, t]) => h('label', { class: id === sound ? 'on' : null }, t,
          h('input', { type: 'radio', name: 'sounds', checked: id === sound, 'data-key': `sounds-${id}`, onchange: () => {
            app.setSoundsOn(id === 'on');
            draw();
          } }))))) : null));
  }
}
