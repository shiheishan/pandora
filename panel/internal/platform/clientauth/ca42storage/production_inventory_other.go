//go:build !linux || (!amd64 && !arm64)

package ca42storage

import "context"

func RetainProductionInventory(context.Context, BoundDescriptor) (*InventoryLease, error) {
	return nil, ErrFSVerityUnsupported
}
