//go:build !darwin && !linux

package monitor

import "fmt"

func CaptureManagedLogs(root string) (func(), error) {
	return nil, fmt.Errorf("managed logging requires macOS or Linux")
}
