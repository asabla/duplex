// Progressive enhancement only: navigation, forms and filters also work as HTML.
function updatePolicy(select) {
  const count = select.closest('form').querySelector('[data-limit]');
  if (!count) {
    return;
  }
  const latest = select.value === 'latest';
  count.hidden = !latest;
  const input = count.querySelector('input');
  input.disabled = !latest;
  input.required = latest;
}

function initialize(root) {
  if (root.id) {
    root = document.getElementById(root.id) || root;
  }
  root.querySelectorAll('select[data-policy]').forEach(updatePolicy);
  root.querySelectorAll('[data-playlist-picker]').forEach(initializePicker);
  const picker = root.closest ? root.closest('[data-playlist-picker]') : null;
  if (picker) {
    initializePicker(picker);
  }
  updateLiveControls();
}

function livePaused() { return sessionStorage.getItem('duplex-live-paused') === 'true'; }
function updateLiveControls() {
  document.querySelectorAll('[data-live-toggle]').forEach(function (button) {
    button.setAttribute('aria-pressed', String(livePaused()));
    button.textContent = livePaused() ? 'Resume live updates' : 'Pause live updates';
  });
}

function protectEdits(event) {
  const form = event.target.closest('form');
  if (form && (form.querySelector('input[name="revision"]') || form.closest('[data-preserve-edits]'))) {
    form.dataset.dirty = 'true';
  }
}

function isEditing(target) {
  return target.querySelector('form[data-dirty="true"], details[open]') ||
    (target.contains(document.activeElement) &&
      document.activeElement.matches('input, select, textarea, button, a, summary'));
}

document.addEventListener('DOMContentLoaded', function () {
  document.documentElement.classList.add('js');
  initialize(document);
});
document.addEventListener('input', protectEdits);
document.addEventListener('change', function (event) {
  protectEdits(event);
  if (event.target.matches('select[data-policy]')) {
    updatePolicy(event.target);
  }
  if (event.target.matches('[data-submit-page-size]')) {
    event.target.form.requestSubmit();
  }
});
document.addEventListener('click', function (event) {
  if (event.target.closest('[data-live-toggle]')) {
    sessionStorage.setItem('duplex-live-paused', String(!livePaused()));
    updateLiveControls();
  }
});
document.addEventListener('submit', function (event) {
  const message = event.target.dataset.confirm;
  if (message && !window.confirm(message)) {
    event.preventDefault();
    event.stopImmediatePropagation();
  }
}, true);
document.addEventListener('htmx:configRequest', function (event) {
  const token = document.querySelector('meta[name="csrf-token"]');
  if (token) {
    event.detail.headers['X-CSRF-Token'] = token.content;
  }
});
document.addEventListener('htmx:beforeRequest', function (event) {
  const target = event.detail.elt;
  if (target.matches('[data-live]') && (livePaused() || isEditing(target))) {
    event.preventDefault();
  }
});
document.addEventListener('htmx:beforeSwap', function (event) {
  if (event.detail.xhr.status >= 400 && event.detail.xhr.status < 500) {
    event.detail.shouldSwap = true;
    event.detail.isError = false;
  }
  const target = event.detail.target;
  if (target && target.matches('[data-preserve-edits]') && isEditing(target)) {
    event.detail.shouldSwap = false;
  }
});
document.addEventListener('htmx:oobAfterSwap', function (event) {
  initialize(event.detail.target);
});
document.addEventListener('htmx:afterSwap', function (event) {
  initialize(event.detail.target);
  const form = event.detail.requestConfig && event.detail.requestConfig.elt.closest('form');
  if (!form || event.detail.target.id !== 'notice') {
    return;
  }
  form.querySelectorAll('[aria-invalid]').forEach(function (field) {
    field.removeAttribute('aria-invalid');
    const message = document.getElementById(field.id + '-error');
    if (message) {
      message.remove();
    }
    const described = (field.getAttribute('aria-describedby') || '').split(' ').filter(function (id) {
      return id !== field.id + '-error';
    });
    field.setAttribute('aria-describedby', described.join(' '));
  });
  const error = event.detail.target.querySelector('[data-field-error]');
  if (error) {
    const input = document.getElementById(error.dataset.fieldError);
    if (input) {
      input.setAttribute('aria-invalid', 'true');
      const message = document.createElement('p');
      message.id = input.id + '-error';
      message.className = 'field-error';
      message.textContent = error.textContent;
      input.insertAdjacentElement('afterend', message);
      input.setAttribute('aria-describedby', ((input.getAttribute('aria-describedby') || '') + ' ' + message.id).trim());
    }
  }
  const summary = document.getElementById('form-errors');
  if (summary) {
    summary.focus();
  }
  const current = document.getElementById(form.id);
  if (event.detail.target.querySelector('[data-saved]') && current) {
    delete current.dataset.dirty;
  }
});

// A bounded native select remains available without JavaScript. With JavaScript,
// it supplies the values for one keyboard-accessible searchable combobox.
function initializePicker(root) {
  const input = root.querySelector('#playlist-search');
  const select = root.querySelector('select[name="playlist"]');
  if (!input || !select) {
    return;
  }
  select.closest('label').hidden = true;
  const previous = root.querySelector('.playlist-options');
  if (previous) {
    previous.remove();
  }
  const list = document.createElement('ul');
  list.id = 'playlist-options-list';
  list.className = 'playlist-options';
  list.setAttribute('role', 'listbox');
  list.setAttribute('aria-label', 'Matching playlists');
  list.hidden = true;
  root.append(list);
  input.setAttribute('role', 'combobox');
  input.setAttribute('aria-autocomplete', 'list');
  input.setAttribute('aria-controls', list.id);
  input.setAttribute('aria-expanded', 'false');
  const options = Array.from(select.options);
  options.forEach(function (option, index) {
    const item = document.createElement('li');
    item.id = 'playlist-option-' + index;
    item.textContent = option.textContent;
    item.setAttribute('role', 'option');
    item.setAttribute('aria-selected', 'false');
    item.addEventListener('mousedown', function (event) { event.preventDefault(); });
    item.addEventListener('click', function () { choose(index); });
    list.append(item);
  });
  let active = -1;
  function close() {
    list.hidden = true;
    input.setAttribute('aria-expanded', 'false');
    input.removeAttribute('aria-activedescendant');
  }
  function choose(index) {
    select.value = options[index].value;
    input.value = select.value ? options[index].textContent : '';
    input.dataset.selectedLabel = input.value;
    close();
    input.focus();
  }
  function open() { list.hidden = false; input.setAttribute('aria-expanded', 'true'); }
  const selected = select.selectedOptions[0];
  input.dataset.selectedLabel = selected && selected.value ? selected.textContent : '';
  if (!document.activeElement || document.activeElement !== input) {
    input.value = input.dataset.selectedLabel;
  }
  input.onfocus = open;
  input.oninput = open;
  input.onblur = function () { close(); input.value = input.dataset.selectedLabel; };
  input.onkeydown = function (event) {
    if (event.key === 'Escape') { close(); event.preventDefault(); return; }
    if (event.key === 'Enter' && !list.hidden && active >= 0) { choose(active); event.preventDefault(); return; }
    if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') {
      return;
    }
    event.preventDefault();
    open();
    active = (active + (event.key === 'ArrowDown' ? 1 : -1) + options.length) % options.length;
    Array.from(list.children).forEach(function (item, index) { item.setAttribute('aria-selected', String(index === active)); });
    input.setAttribute('aria-activedescendant', list.children[active].id);
    list.children[active].scrollIntoView({ block: 'nearest' });
  };
  if (document.activeElement === input) {
    open();
  }
}
