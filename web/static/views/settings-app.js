// «Настройки → Приложение» (спека этапа 13, 5.2): только в приложении для Android — плеер каналов (встроенный
// или системный: VLC и другие плееры Android) и версия приложения. Настройка хранится на устройстве.
import { h, fill } from '../ui.js';
import { layout } from './settings-layout.js';
import { appBridge } from './tvkit.js';

const PLAYERS = [['builtin', 'Встроенный'], ['system', 'Системный (VLC и другие)']];

export function render(root) {
  const app = appBridge();
  if (!app) {
    location.replace('#/settings/status'); // в браузере раздела нет
    return;
  }
  const body = h('div');
  layout(root, 'app', 'Приложение').append(body);
  draw();

  function draw() {
    // У приложения 13a настройки плеера нет — только версия.
    const canSet = typeof app.player === 'function' && typeof app.setPlayer === 'function';
    const mode = canSet ? app.player() : '';
    fill(body,
      canSet ? h('section', { class: 'card' }, h('div', { class: 'h' }, 'Плеер каналов'),
        h('div', { class: 'seg', role: 'radiogroup', 'aria-label': 'Плеер каналов' }, PLAYERS.map(([id, t]) => h('label', { class: id === mode ? 'on' : null }, t,
          h('input', { type: 'radio', name: 'player', checked: id === mode, 'data-key': `player-${id}`, onchange: () => {
            app.setPlayer(id);
            draw();
          } }))))) : null,
      h('p', { class: 'muted' }, 'Версия приложения ' + (typeof app.version === 'function' ? app.version() : '')));
  }
}
