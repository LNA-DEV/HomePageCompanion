// Builds the admin UIs of building blocks with the shell's toolchain
// (docs/concepts/modular-architecture.md §5.7). A module's UI is
// <module>/ui/src/entry.ts (or .js); files named *.ce.svelte become custom
// elements. The sources are staged under web/.modules/ so they resolve
// svelte and @companion/ui from here, and each bundle carries its own
// Tailwind CSS, without the base styles the shell already has.
//
//   node scripts/build-modules.mjs --out <dir> [<module dir>...]
//   node scripts/build-modules.mjs --stage-only     (for svelte-check)
//
// Without module dirs it builds every ../modules/*/. The output is
// <out>/<module path with / as __>/entry.js, which hpc build embeds.
import { build } from 'vite';
import { svelte, vitePreprocess } from '@sveltejs/vite-plugin-svelte';
import tailwind from 'tailwindcss';
import autoprefixer from 'autoprefixer';
import { cp, mkdir, readdir, readFile, rm, stat, writeFile } from 'node:fs/promises';
import { existsSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import shellTailwind from '../tailwind.config.js';

const web = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const args = process.argv.slice(2);
let out = '';
let stageOnly = false;
const dirs = [];
for (let i = 0; i < args.length; i++) {
	if (args[i] === '--out') out = path.resolve(args[++i]);
	else if (args[i] === '--stage-only') stageOnly = true;
	else dirs.push(path.resolve(args[i]));
}
if (!out && !stageOnly) {
	console.error('usage: build-modules.mjs --out <dir> [<module dir>...]');
	process.exit(2);
}
if (dirs.length === 0) {
	const root = path.join(web, '..', 'modules');
	for (const name of (await readdir(root)).sort()) dirs.push(path.join(root, name));
}

async function modulePath(dir) {
	const gomod = await readFile(path.join(dir, 'go.mod'), 'utf8');
	const m = gomod.match(/^module\s+(\S+)/m);
	if (!m) throw new Error(`${dir}/go.mod has no module line`);
	return m[1];
}

function entryOf(ui) {
	for (const name of ['entry.ts', 'entry.js']) {
		if (existsSync(path.join(ui, 'src', name))) return 'src/' + name;
	}
	return null;
}

const staging = path.join(web, '.modules');
await rm(staging, { recursive: true, force: true });
await mkdir(staging, { recursive: true });
// For svelte-check --workspace .modules: module elements compile as
// custom elements.
await writeFile(
	path.join(staging, 'svelte.config.js'),
	"import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';\n" +
		'export default { preprocess: vitePreprocess(), compilerOptions: { customElement: true } };\n'
);
let built = 0;
for (const dir of dirs) {
	const ui = path.join(dir, 'ui');
	const entry = existsSync(ui) && (await stat(ui)).isDirectory() ? entryOf(ui) : null;
	if (!entry) continue;
	const key = (await modulePath(dir)).replaceAll('/', '__');
	const stage = path.join(staging, key);
	await cp(path.join(ui, 'src'), path.join(stage, 'src'), { recursive: true });
	if (stageOnly) continue;
	await build({
		configFile: false,
		root: stage,
		logLevel: 'warn',
		plugins: [
			svelte({
				preprocess: vitePreprocess(),
				dynamicCompileOptions: ({ filename }) =>
					filename.endsWith('.ce.svelte') ? { customElement: true } : undefined
			})
		],
		resolve: {
			alias: {
				'@companion/ui/components': path.join(web, 'src/sdk/components.ts'),
				'@companion/ui': path.join(web, 'src/sdk/index.ts'),
				$lib: path.join(web, 'src/lib')
			}
		},
		css: {
			postcss: {
				plugins: [
					tailwind({
						...shellTailwind,
						content: [path.join(stage, 'src/**/*.{svelte,ts,js}'), path.join(web, 'src/sdk/**/*.ts'), path.join(web, 'src/lib/components/**/*.svelte')],
						corePlugins: { preflight: false }
					}),
					autoprefixer()
				]
			}
		},
		build: {
			outDir: path.join(out, key),
			emptyOutDir: true,
			lib: { entry: path.join(stage, entry), formats: ['es'], fileName: () => 'entry.js' },
			cssCodeSplit: false,
			sourcemap: false
		}
	});
	built++;
	console.log(`built ${key}`);
}
console.log(stageOnly ? 'staged the module UIs' : `built ${built} module UI(s) into ${out}`);
