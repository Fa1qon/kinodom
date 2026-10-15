// Настройки приложения: режимы воспроизведения фильмов и ТВ хранятся отдельно.
import { h, fill, keepFocus } from '../ui.js';
import { get } from '../api.js';
import { layout } from './settings-layout.js';
import { appBridge, installedPlayers } from './tvkit.js';
import { isClient } from '../client-ui.js';
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
  const pairing = isClient() ? null : h('section', { class: 'card' }, h('div', { class: 'h' }, 'Подключение ТВ'), h('p', { class: 'muted small' }, 'Введите этот код в приложении Kinodom на телевизоре. Он действует 10 минут.'));
  layout(root, 'app', 'Приложение').append(body, pairing, update);
  draw();
  get('/app').then((info) => fill(update, appCard(appModel(info, [], app), app) || version()), () => {});
  if (pairing) get('/pairing').then((info) => fill(pairing,
    h('div', { class: 'pair-code', 'aria-label': 'Код подключения ТВ' }, info.code),
    h('p', { class: 'muted small' }, `Действует до ${new Date(info.expiresAt).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}`)),
  () => fill(pairing, h('p', { class: 'muted small' }, 'Код доступен только на этом компьютере.')));

  function radioCard(title, mode, group, key, setMode, values = PLAYERS, note = null) {
    const labels = values.map(([id, text]) => {
      const label = h('label', { class: id === mode ? 'on' : null }, text);
      label.append(h('input', {
        type: 'radio', name: group, checked: id === mode, 'data-key': key + '-' + id,
        onchange: () => { setMode(id); draw(); },
      }));
      return label;
    });
    return h('section', { class: 'card' }, h('div', { class: 'h' }, title),
      note ? h('p', { class: 'muted small' }, note) : null,
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
    // Сервер на устройстве (полный порт, план 2026-10-06): пункт только в сборках с сервером.
    const localCap = typeof app.localServer === 'function' && typeof app.setLocalServer === 'function' && app.localServer() !== 'none';
    const players = isClient() ? installedPlayers(app) : null;
    keepFocus(body, () => fill(body,
      localCap ? radioCard('Сервер на этом устройстве', app.localServer() === 'on' ? 'on' : 'off', 'local-server', 'local-server',
        id => { app.setLocalServer(id === 'on'); draw(); }, [['on', 'Включён'], ['off', 'Выключен']],
        'Кино смотрят с этого телевизора и без сервера в сети: загрузки и медиатека — в памяти устройства.') : null,
      movieCap || movieLegacy ? radioCard('Плеер фильмов', movie, 'movie-playback-mode', 'movie-player', id => movieCap ? app.setMoviePlaybackMode(id) : app.setMoviePlayer(id)) : null,
      channelCap || channelLegacy ? radioCard('Плеер каналов', channel, 'channel-playback-mode', 'channel-player', id => channelCap ? app.setChannelPlaybackMode(id) : app.setPlaybackMode(id)) : null,
      isClient() ? h('section', { class: 'card' }, h('div', { class: 'h' }, 'Внешние плееры на этом устройстве'),
        h('p', { class: 'muted small' }, players === null ? 'Список плееров недоступен.'
          : players.length ? players.map(p => p.name).join(', ') : 'Внешние плееры не установлены.'),
        h('p', { class: 'muted small' }, 'В системном режиме Android предложит выбрать установленное приложение при начале просмотра.')) : null,
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
