import globals from 'globals';

const rules = Object.fromEntries(
  [
    'no-undef',
    'no-unused-vars',
    'no-unreachable',
    'no-unsafe-finally',
    'no-unsafe-optional-chaining',
    'no-async-promise-executor',
    'no-promise-executor-return',
    'valid-typeof',
    'constructor-super',
    'no-dupe-args',
    'no-dupe-keys',
    'no-dupe-else-if',
    'no-constant-binary-expression',
    'no-unexpected-multiline',
    'no-loss-of-precision',
    'eqeqeq',
  ].map((name) => [name, 'error']),
);

export default [
  { ignores: ['internal/admin/static/htmx.min.js'] },
  {
    files: ['internal/admin/static/*.js'],
    languageOptions: {
      sourceType: 'script',
      globals: { ...globals.browser, htmx: 'readonly' },
    },
    rules,
  },
  {
    files: ['tools/readability/*.mjs'],
    languageOptions: { sourceType: 'module', globals: globals.node },
    rules,
  },
];
