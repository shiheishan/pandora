//go:build !linux

package ca42storage

func platformInventoryOps() inventoryOps { return inventoryOps{} }
