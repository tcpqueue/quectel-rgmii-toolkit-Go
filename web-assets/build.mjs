import { build } from 'esbuild';
import { mkdir, copyFile, readFile, writeFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('.', import.meta.url));
await build({ absWorkingDir: root, entryPoints: ['vendor.js'], bundle: true, minify: true, format: 'iife', target: ['es2020'], legalComments: 'eof', outfile: '../development/simpleadmin/www/js/monitor-vendor.js' });
const bundle = new URL('../development/simpleadmin/www/js/monitor-vendor.js', import.meta.url);
await writeFile(bundle, (await readFile(bundle, 'utf8')).replace(/[\t ]+$/gm, ''));
const licenses = new URL('../development/simpleadmin/www/licenses/', import.meta.url);
await mkdir(licenses, { recursive: true });
for (const [source, destination] of [['node_modules/echarts/LICENSE', 'echarts-LICENSE.txt'], ['node_modules/echarts/NOTICE', 'echarts-NOTICE.txt'], ['node_modules/lucide/LICENSE', 'lucide-LICENSE.txt']]) {
  await copyFile(new URL(source, import.meta.url), new URL(destination, licenses));
}
