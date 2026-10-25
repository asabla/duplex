// Validate controlled rendered pages and HTMX fragments without retaining bodies.
import { readFile, readdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { HtmlValidate } from 'html-validate';

const expected = JSON.parse(
  await readFile(new URL('./package.json', import.meta.url), 'utf8'),
);
for (const name of [
  'html-validate',
  'prettier',
  'prettier-plugin-go-template',
]) {
  const installed = JSON.parse(
    await readFile(
      new URL(`./node_modules/${name}/package.json`, import.meta.url),
      'utf8',
    ),
  );
  if (installed.version !== expected.devDependencies[name]) {
    throw new Error(`Install the pinned ${name} dependency with npm ci.`);
  }
}

const directory = process.argv[2];
if (!directory) {
  throw new Error('Supply the controlled rendered-markup directory.');
}
const files = (await readdir(directory))
  .filter((file) => file.endsWith('.html'))
  .sort();
if (!files.length) {
  throw new Error('No rendered markup was supplied.');
}
const validator = new HtmlValidate({
  root: true,
  elements: ['html5'],
  rules: { 'input-missing-label': 'error', 'prefer-native-element': 'error' },
});
const reports = [];
for (const file of files) {
  const report = await validator.validateFile(path.join(directory, file));
  reports.push({
    file,
    errors: report.errorCount,
    warnings: report.warningCount,
    messages: report.results.flatMap((result) =>
      result.messages.map(({ ruleId, message, line, column }) => ({
        ruleId,
        message,
        line,
        column,
      })),
    ),
  });
}
const result = {
  pages: reports.length,
  errors: reports.reduce((sum, item) => sum + item.errors, 0),
  reports,
};
await writeFile(
  path.join(directory, 'markup-validation.json'),
  JSON.stringify(result, null, 2) + '\n',
);
console.log(
  `Rendered HTML: ${result.pages} pages/fragments, ${result.errors} errors.`,
);
if (result.errors) {
  console.error(
    JSON.stringify(
      reports.filter((report) => report.errors),
      null,
      2,
    ),
  );
  process.exitCode = 1;
}
