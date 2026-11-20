// ServeRPCMethods is the forever serve method table for the `forever rpc`
// client: the DONE names it answers with ForeverCore code and the
// NOT-PORTED names it answers with the typed not-ported error. Both lists
// come from the one registration site (registerServeMethods), so the
// client list can never drift from the socket.
package world

import (
	"sort"

	"github.com/pjerra/forever-agent/internal/rpc"
)

// ServeNotPortedError is the typed answer of a method this server does not
// implement: a caller gets the name and the reason, never an empty success.
func ServeNotPortedError(method string) error {
	return notPorted(method)
}

// ServeRPCMethods returns the serve method table: the DONE names first, the
// NOT-PORTED names second, each sorted.
func ServeRPCMethods() (ported, notPortedNames []string) {
	srv := rpc.NewServer()
	registerServeMethods(srv, &serveState{})
	np := make(map[string]bool, len(notPortedMethods))
	for _, n := range notPortedMethods {
		np[n] = true
	}
	for _, n := range srv.Methods() {
		if np[n] {
			notPortedNames = append(notPortedNames, n)
		} else {
			ported = append(ported, n)
		}
	}
	sort.Strings(ported)
	sort.Strings(notPortedNames)
	return ported, notPortedNames
}
