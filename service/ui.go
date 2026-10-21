package service

import (
	"html/template"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"

	"github.com/LNA-DEV/HomePageCompanion/companion"
)

// serveUI answers /ui/<file> with the building block's UI files. The shell
// shows them in a sandboxed iframe (docs/concepts/modular-architecture.md
// §5.7) and asks for index.html?element=<tag>&path=<rest>; a module's UI has
// no index.html of its own, so one is made that loads entry.js and places
// the element. Its @companion/ui talks to the shell over postMessage.
func (s *Service) serveUI(w http.ResponseWriter, r *http.Request, file string) {
	files := companion.UIFiles(s.info)
	if files == nil {
		http.NotFound(w, r)
		return
	}
	file = strings.TrimPrefix(path.Clean("/"+file), "/")
	if file == "" {
		file = "index.html"
	}
	data, err := fs.ReadFile(files, file)
	if err != nil && file == "index.html" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		framePage.Execute(w, s.info.ID)
		return
	}
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ct := mime.TypeByExtension(path.Ext(file))
	if ct == "" {
		ct = http.DetectContentType(data)
	}
	w.Header().Set("Content-Type", ct)
	w.Write(data)
}

// framePage places one custom element of the module's UI.
var framePage = template.Must(template.New("frame").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<script type="module" src="entry.js"></script>
<style>body{margin:0;font-family:system-ui,sans-serif}</style>
</head>
<body>
<div id="root" data-module="{{.}}"></div>
<script type="module">
const q = new URLSearchParams(location.search);
const tag = q.get('element') || q.get('widget');
if (tag && /^[a-z][a-z0-9]*-[a-z0-9-]*$/.test(tag)) {
	customElements.whenDefined(tag).then(() => {
		const el = document.createElement(tag);
		el.dataset.module = {{.}};
		if (q.get('path')) el.setAttribute('path', q.get('path'));
		document.getElementById('root').replaceChildren(el);
	});
}
</script>
</body>
</html>
`))
