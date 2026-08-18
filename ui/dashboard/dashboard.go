package dashboard

import _ "embed"

//go:embed index.html
var Index []byte

//go:embed app.css
var CSS []byte

//go:embed app.js
var JS []byte

//go:embed flowwweb-icon.png
var Icon []byte

//go:embed flow-wordmark.png
var Wordmark []byte

func Asset(name string) ([]byte, string, bool) {
	switch name {
	case "app.css":
		return CSS, "text/css; charset=utf-8", true
	case "app.js":
		return JS, "text/javascript; charset=utf-8", true
	case "flowwweb-icon.png":
		return Icon, "image/png", true
	case "flow-wordmark.png":
		return Wordmark, "image/png", true
	default:
		return nil, "", false
	}
}
