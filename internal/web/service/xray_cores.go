package service

import (
	"embed"
	"runtime"
)

// Bundled Xray cores let the version picker switch with no network access.
// Only linux/amd64 is bundled; other platforms fall back to the download path
// in UpdateXray. Regenerate the zips with:
//
//	curl -fL -o internal/web/service/cores/Xray-linux-64-<ver>.zip \
//	  https://github.com/XTLS/Xray-core/releases/download/<ver>/Xray-linux-64.zip
//
//go:embed cores/*.zip
var bundledCoreFS embed.FS

var bundledCoreZips = map[string]string{
	"v26.9.9":  "cores/Xray-linux-64-v26.9.9.zip",
	"v25.12.2": "cores/Xray-linux-64-v25.12.2.zip",
	"v25.6.7":  "cores/Xray-linux-64-v25.6.7.zip",
}

// bundledCoreZip returns the embedded release zip for version when the running
// platform matches the bundled artifact (linux/amd64).
func bundledCoreZip(version string) ([]byte, bool) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return nil, false
	}
	p, ok := bundledCoreZips[version]
	if !ok {
		return nil, false
	}
	b, err := bundledCoreFS.ReadFile(p)
	if err != nil {
		return nil, false
	}
	return b, true
}
