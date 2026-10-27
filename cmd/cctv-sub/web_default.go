//go:build !standalone

package main

import "net/http"

// Docker and API-only builds keep the existing separate Web service.
func webHandler() http.Handler {
	return nil
}
