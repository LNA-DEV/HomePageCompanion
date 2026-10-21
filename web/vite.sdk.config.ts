// @companion/ui as one ES module for service UIs written without the
// shell's toolchain (a Python service's static page):
// import { connect } from '/ui/sdk/companion-ui.js'. The core serves it from
// the shell's build.
import { defineConfig } from 'vite';

export default defineConfig({
	build: {
		lib: { entry: 'src/sdk/index.ts', formats: ['es'], fileName: () => 'companion-ui.js' },
		outDir: 'build/ui-sdk',
		emptyOutDir: true,
		minify: true
	}
});
