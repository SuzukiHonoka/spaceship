//go:build !linux && !darwin && !windows

package netmon

type unsupportedMonitor struct{}

func newPlatformMonitor(string) (Monitor, error) {
	return unsupportedMonitor{}, nil
}

func (unsupportedMonitor) Current() (DefaultInterface, error) {
	return DefaultInterface{}, ErrNoDefaultInterface
}

func (unsupportedMonitor) Start(func(DefaultInterface)) error {
	return nil
}

func (unsupportedMonitor) Close() error {
	return nil
}
