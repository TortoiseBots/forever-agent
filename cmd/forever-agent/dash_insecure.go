package main

import (
	"fmt"
	"io"
	"strings"
)

// warnInsecureDash prints the one line a plain-http dashboard gets when --dash-insecure-http
// allowed it. The boundary: the login sends a GM password and the session cookie, and plain
// http puts both on the wire in clear text; loopback is the only host that needs no warning.
func warnInsecureDash(w io.Writer, command, url string) {
	if strings.HasPrefix(strings.ToLower(url), "http://") {
		fmt.Fprintf(w, "%s: warning: plain HTTP dashboard: the GM password and the session cookie travel in clear text\n", command)
	}
}
