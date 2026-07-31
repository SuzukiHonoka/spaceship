//go:build !linux

package tun

func Supported() bool {
	return false
}

func openTUNDevice(Config, func(error)) (openedDevice, error) {
	return openedDevice{}, ErrUnsupported
}
