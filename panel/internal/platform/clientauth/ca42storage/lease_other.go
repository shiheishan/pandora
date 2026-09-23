//go:build !linux

package ca42storage

func leasePlatformSupported() bool { return false }

func platformLeaseOps() leaseOps { return leaseOps{} }
