// Настройки приложения: режимы воспроизведения фильмов и ТВ хранятся отдельно.
import { h, fill, keepFocus } from '../ui.js';
import { get } from '../api.js';
import { layout } from './settings-layout.js';
import { appBridge } from './tvkit.js';
import { appModel, appCard } from './settings-status.js';

const PLAYERS = [['builtin', 'Встроенный'], ['system', 'Системный (выбор приложения)']];
const SOUNDS = [['on', 'Включены'], ['off', 'Выключены']];

export function render(root) {
  const app = appBridge();
  if (!app) {
    location.replace('#/settings/status');
    return;
  }
  const version = () => h('p', { class: 'muted' }, 'Версия приложения ' + (typeof app.version === 'function' ? app.version() : ''));
  const body = h('div');
  const update = h('div', null, version());
  layout(root, 'app', 'Приложение').append(body, update);
  draw();
  get('/app').then((info) => fill(update, appCard(appModel(info, [], app), app) || version()), () => {});

  function radioCard(title, mode, group, key, setMode) {
    const labels = PLAYERS.map(([id, text]) => {
      const label = h('label', { class: id === mode ? 'on' : null }, text);
      label.append(h('input', {
        type: 'radio', name: group, checked: id === mode, 'data-key': key + '-' + id,
        onchange: () => { setMode(id); draw(); },
      }));
      return label;
    });
    return h('section', { class: 'card' }, h('div', { class: 'h' }, title),
      h('div', { class: 'seg', role: 'radiogroup', 'aria-label': title }, ...labels));
  }

  function draw() {
    const movieCap = typeof app.moviePlaybackMode === 'function' && typeof app.setMoviePlaybackMode === 'function';
    const channelCap = typeof app.channelPlaybackMode === 'function' && typeof app.setChannelPlaybackMode === 'function';
    const movieLegacy = typeof app.moviePlayer === 'function' && typeof app.setMoviePlayer === 'function';
    const channelLegacy = typeof app.playbackMode === 'function' && typeof app.setPlaybackMode === 'function';
    const movie = movieCap ? app.moviePlaybackMode() : (movieLegacy ? app.moviePlayer() : 'builtin');
    const channel = channelCap ? app.channelPlaybackMode() : (channelLegacy ? app.playbackMode() : 'builtin');
    const canSound = typeof app.soundsOn === 'function' && typeof app.setSoundsOn === 'function';
    const sound = canSound && app.soundsOn() ? 'on' : 'off';
    keepFocus(body, () => fill(body,
      movieCap || movieLegacy ? radioCard('Плеер фильмов', movie, 'movie-playback-mode', 'movie-player', id => movieCap ? app.setMoviePlaybackMode(id) : app.setMoviePlayer(id)) : null,
      channelCap || channelLegacy ? radioCard('Плеер каналов', channel, 'channel-playback-mode', 'channel-player', id => channelCap ? app.setChannelPlaybackMode(id) : app.setPlaybackMode(id)) : null,
      canSound ? radioSound(sound) : null));
  }

  function radioSound(mode) {
    const labels = SOUNDS.map(([id, text]) => {
      const label = h('label', { class: id === mode ? 'on' : null }, text);
      label.append(h('input', { type: 'radio', name: 'sounds', checked: id === mode, 'data-key': `sounds-${id}`, onchange: () => { app.setSoundsOn(id === 'on'); draw(); } }));
      return label;
    });
    return h('section', { class: 'card' }, h('div', { class: 'h' }, 'Звуки меню'),
      h('div', { class: 'seg', role: 'radiogroup', 'aria-label': 'Звуки меню' }, ...labels));
  }
}

// Legacy label: 'Плеер фильмов и каналов' remains for old clients.