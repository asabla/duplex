function updatePolicy(select) {
  const form = select.closest('form');
  const count = form.querySelector('[data-limit]');
  if (!count) return;
  const latest = select.value === 'latest';
  count.hidden = !latest;
  const input = count.querySelector('input');
  input.disabled = !latest;
  input.required = latest;
}

function initializePolicies(root) {
  root.querySelectorAll('select[data-policy]').forEach(updatePolicy);
}

document.addEventListener('DOMContentLoaded', function () {
  initializePolicies(document);
});
document.addEventListener('change', function (event) {
  if (event.target.matches('select[data-policy]')) updatePolicy(event.target);
});
document.addEventListener('htmx:afterSwap', function (event) {
  initializePolicies(event.detail.target);
});
document.addEventListener('htmx:configRequest', function (event) {
  const token = document.querySelector('meta[name="csrf-token"]');
  if (token) event.detail.headers['X-CSRF-Token'] = token.content;
});
document.addEventListener('htmx:beforeSwap', function (event) {
  if (event.detail.xhr.status >= 400 && event.detail.xhr.status < 500) {
    event.detail.shouldSwap = true;
    event.detail.isError = false;
  }
});

// A background refresh must not discard an edit or move keyboard focus.
function protectEdits(event) {
  const form = event.target.closest('form');
  if (form && (form.querySelector('input[name="revision"]') ||
    form.closest('[data-preserve-edits]'))) {
    form.dataset.dirty = 'true';
  }
}

document.addEventListener('input', protectEdits);
document.addEventListener('change', protectEdits);
document.addEventListener('htmx:beforeSwap', function (event) {
  const target = event.detail.target;
  if (!target || !target.matches('[data-preserve-edits]')) return;
  const editing = target.querySelector('form[data-dirty="true"]');
  const focused = target.contains(document.activeElement) &&
    document.activeElement.matches('input, select, textarea, button, a, summary');
  if (editing || focused) {
    event.detail.shouldSwap = false;
  }
});
