//go:build (windows || darwin) && standalone

package ble

import (
	"context"
	"embed"
)

// The buildstandalone command stages the platform native helper here.
//
//go:embed bundle/*
var bundledHelper embed.FS

func prepareHelper(executable, workdir, override, goos string) (string, func(), error) {
	return prepareHelperContext(context.Background(), executable, workdir, override, goos)
}
func prepareHelperContext(ctx context.Context, executable, workdir, override, goos string) (string, func(), error) {
	if err := ctx.Err(); err != nil {
		return "", func() {}, err
	}
	if override != "" {
		path, err := resolveHelper(executable, workdir, override, goos)
		return path, func() {}, err
	}
	name := "btfling-ble"
	if goos == "windows" {
		name += ".exe"
	}
	data, err := bundledHelper.ReadFile("bundle/" + name)
	if err != nil {
		return "", func() {}, err
	}
	return extractHelper(data, name)
}
