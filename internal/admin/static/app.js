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
