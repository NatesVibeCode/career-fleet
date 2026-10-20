//go:build !unix

package egresscontract

// This inherited descriptor transport requires the native Unix pipe boundary.
// Explicit selection refuses on other hosts; direct HTTPS is a separate mode.
func prepareProviderHTTPSInheritedPipes() error { return ErrProviderHTTPS }
