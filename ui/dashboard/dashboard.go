package dashboard

import _ "embed"

//go:embed index.html
var Index []byte

//go:embed app.css
var CSS []byte

//go:embed app.js
var JS []byte

//go:embed flowwweb-mark.svg
var Mark []byte

func Asset(name string) ([]byte, string, bool) {
	switch name {
	case "app.css":
		return CSS, "text/css; charset=utf-8", true
	case "app.js":
		return JS, "text/javascript; charset=utf-8", true
	case "flowwweb-mark.svg":
		return Mark, "image/svg+xml", true
	default:
		return nil, "", false
	}
}
