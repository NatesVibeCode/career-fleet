//go:build !darwin && !linux

package egresscontract

func immutableMaterialPath(string) bool { return false }
