// Особенности клиента включаются только по явному признаку Android-моста.
export function isClient() {
  const app = typeof window !== 'undefined' && window.KinodomApp;
  return !!(app && typeof app.distribution === 'function' && app.distribution() === 'client');
}

const guarded = new WeakSet();
const selector = 'input:not([type]), input[type="text"], input[type="password"], input[type="url"], input[type="email"], input[type="number"], input[type="search"], input[type="tel"], textarea';

export function prepareClientInput(el) {
  if (!isClient() || !/^#\/((settings|setup)(\/|$))/.test(location.hash) || !el.matches(selector) || guarded.has(el) || el.readOnly) return;
  guarded.add(el);
  el.readOnly = true;
  el.dataset.clientInput = 'true';
  const edit = () => {
    if (!el.readOnly) return;
    // Повторный фокус после снятия readonly открывает клавиатуру WebView.
    el.blur();
    el.readOnly = false;
    el.focus({ preventScroll: true });
  };
  el.addEventListener('click', edit);
  el.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && el.readOnly) {
      e.preventDefault();
      e.stopPropagation();
      edit();
    } else if (e.key === 'Escape' && !el.readOnly) {
      el.readOnly = true;
    }
  });
  el.addEventListener('blur', () => { el.readOnly = true; });
}

export function initClientUI() {
  if (!isClient()) return;
  document.body.classList.add('client-ui');
  function prepare() {
    document.querySelectorAll(selector).forEach(prepareClientInput);
  }
  new MutationObserver(prepare).observe(document.body, { childList: true, subtree: true });
  prepare();
}
