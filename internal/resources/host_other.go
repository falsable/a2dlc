//go:build !linux && !windows && !darwin

package resources

import "errors"

func cgroupCPUs() (float64, bool) { return 0, false }
func loadAvg() (float64, bool)    { return 0, false }
func memory() (int64, int64, error) {
	return 0, 0, errors.New("memory is not measured on this OS")
}
