package web

import (
	"os/exec"
	"testing"
)

func TestClientInputActivation(t *testing.T) {
	script := `
import assert from 'node:assert/strict';
import { isClient, prepareClientInput } from './client-ui.js';
import { arrowMoves, focusScrollDelta } from './nav.js';
import { installedPlayers } from './views/tvkit.js';
assert.deepEqual(installedPlayers({ installedPlayers: () => '[{"id":"org.tv.player","name":"TV Player"}]' }), [{ id: 'org.tv.player', name: 'TV Player' }]);
assert.deepEqual(installedPlayers({ installedPlayers: () => '[]' }), []);
assert.equal(installedPlayers({}), null);
assert.equal(installedPlayers({ installedPlayers: () => 'invalid' }), null);
assert.equal(installedPlayers({ installedPlayers: () => '{}' }), null);
globalThis.window = { KinodomApp: { distribution: () => 'client' } };
globalThis.location = { hash: '#/settings/params' };
function field() {
  const listeners = {};
  return { tagName: 'INPUT', type: 'text', dataset: {}, readOnly: false,
    value: 'server', selectionStart: 3, selectionEnd: 3,
    matches: () => true, addEventListener: (name, fn) => { listeners[name] = fn; },
    blur() { listeners.blur?.(); }, focus() { this.focusedEditable = !this.readOnly; }, listeners };
}
const el = field();
prepareClientInput(el);
assert.equal(el.readOnly, true);
assert.equal(arrowMoves('left', el, null), true);
let prevented = false;
el.listeners.keydown({ key: 'Enter', preventDefault() { prevented = true; }, stopPropagation() {} });
assert.equal(prevented, true);
assert.equal(el.focusedEditable, true);
assert.equal(arrowMoves('left', el, null), false);
el.blur();
assert.equal(el.readOnly, true);
el.listeners.click();
assert.equal(el.readOnly, false);
el.listeners.keydown({ key: 'Escape' });
assert.equal(el.readOnly, true);
location.hash = '#/search';
const search = field(); prepareClientInput(search);
assert.equal(search.readOnly, false);
location.hash = '#/settings/params';
window.KinodomApp.distribution = () => 'full';
assert.equal(isClient(), false);
assert.equal(focusScrollDelta({ top: 0, bottom: 20 }, 1000), -25);
assert.equal(focusScrollDelta({ top: 980, bottom: 1020 }, 1000), 45);
assert.equal(focusScrollDelta({ top: 200, bottom: 300 }, 1000), 0);
const full = field(); prepareClientInput(full);
assert.equal(full.readOnly, false);
delete window.KinodomApp;
assert.equal(isClient(), false);
`
	cmd := exec.Command(lookNode(t), "--input-type=module", "--no-warnings", "-e", script)
	cmd.Dir = "static"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}
