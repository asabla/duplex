// The server compares the submitted rules with the preview for plain HTML too.
// These controls make obsolete previews and ignored inputs clear before submit.
document.addEventListener('DOMContentLoaded', function () {
  const form = document.querySelector('[data-selection-editor]');
  if (!form) {
    return;
  }

  function updateContentRules() {
    const separate = form.querySelector('[data-separate-content]').checked;
    form.querySelector('[data-content-rules]').hidden = !separate;
    form.querySelectorAll('[data-content-row]').forEach(function (row) {
      const mode = row.querySelector('[data-content-mode]');
      const limit = row.querySelector('[data-content-limit]');
      const latest = separate && mode.value === 'latest';
      mode.disabled = !separate;
      limit.hidden = !latest;
      limit.querySelector('input').disabled = !latest;
    });
  }

  function changeRules(event) {
    if (!event.target.matches('[data-selection-rule]')) {
      return;
    }
    updateContentRules();
    const save = form.querySelector('[data-selection-save]');
    if (save) {
      save.disabled = true;
      form.querySelector('[data-preview-outdated]').hidden = false;
    }
  }

  form.addEventListener('input', changeRules);
  form.addEventListener('change', changeRules);
  form.addEventListener('submit', function () {
    // Preserve ignored values across review/search posts. The server ignores
    // them under All, Skip, or shared selection, and validates active limits.
    form.querySelectorAll('[data-selection-rule]').forEach(function (input) {
      input.disabled = false;
    });
  });
  updateContentRules();

  const errors = document.querySelector('[data-focus-errors]');
  const preview = document.getElementById('selection-preview-title');
  if (errors) {
    errors.focus();
  } else if (preview && location.hash === '#selection-preview-title') {
    preview.focus({ preventScroll: true });
  }
});
