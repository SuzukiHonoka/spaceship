//go:build !linux && !darwin && !windows

package tun

func Supported() bool {
	return false
}

func openTUNDevice(Config, func(error)) (openedDevice, error) {
	return openedDevice{}, ErrUnsupported
}
