//go:build unix

package egresscontract

import "syscall"

func prepareProviderHTTPSInheritedPipes() error {
	if e := syscall.SetNonblock(3, true); e != nil {
		return e
	}
	return syscall.SetNonblock(4, true)
}
