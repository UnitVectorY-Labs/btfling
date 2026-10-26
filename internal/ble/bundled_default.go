//go:build (!windows && !darwin) || !standalone

package ble

import "context"

func prepareHelper(executable, workdir, override, goos string) (string, func(), error) {
	return prepareHelperContext(context.Background(), executable, workdir, override, goos)
}
func prepareHelperContext(ctx context.Context, executable, workdir, override, goos string) (string, func(), error) {
	if override == "" && (goos == "darwin" || goos == "windows") && goBuildExecutable(executable) && sourceCheckout(workdir) {
		path, err := sourceHelper(ctx, workdir, goos)
		return path, func() {}, err
	}
	path, err := resolveHelper(executable, workdir, override, goos)
	return path, func() {}, err
}
