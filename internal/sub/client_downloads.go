package sub

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

// clientDownloadPath is the URL prefix the panel serves mirrored installers
// from. It is registered on the subscription server rather than the panel
// server because the download buttons live on the subscription page: pointing
// at the same host and port means the links keep working when the panel is
// only reachable through the subscription port (behind Cloudflare, for
// example), and it needs no extra listener.
const clientDownloadPath = "/clients/"

// clientFilePattern is the whole path-traversal defence for these routes: a
// request may only name a bare file inside the clients directory. Anything
// with a separator, a leading dot or non-ASCII is rejected outright instead of
// being cleaned and retried.
var clientFilePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// clientDownload is one installable client shown on the subscription page.
// File is the mirrored file name inside <db folder>/clients. Apps that are
// distributed only through an app store keep File empty and are linked to
// their official page — that is the only place their installer may be
// obtained, so the panel must not pretend to mirror them.
type clientDownload struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Platform string `json:"platform"`
	Version  string `json:"version,omitempty"`
	File     string `json:"file,omitempty"`
	Official string `json:"official,omitempty"`
	URL      string `json:"url"`
}

// clientCatalog is the single source of truth for the download list. Order
// here is the order rendered on the page: open-source mirrors first, then the
// store-only clients.
var clientCatalog = []clientDownload{
	{
		Key: "v2rayng", Label: "V2RayNG", Platform: "android",
		Version:  "2.3.15",
		File:     "v2rayng-universal.apk",
		Official: "https://github.com/2dust/v2rayNG/releases",
	},
	{
		Key: "singbox", Label: "Sing-box", Platform: "android",
		Version:  "1.14.4",
		File:     "singbox-sfa.apk",
		Official: "https://github.com/SagerNet/sing-box-for-android",
	},
	{
		Key: "happ", Label: "Happ", Platform: "android",
		Version:  "4.10.1",
		File:     "happ.apk",
		Official: "https://github.com/Happ-proxy/happ-android/releases",
	},
	{
		Key: "incy", Label: "Incy", Platform: "android",
		Version:  "3.9.9",
		File:     "incy.apk",
		Official: "https://github.com/INCY-DEV/incy-platforms/releases",
	},
	{
		Key: "v2box-android", Label: "V2Box", Platform: "android",
		Official: "https://play.google.com/store/apps/details?id=dev.hexasoftware.v2box",
	},
	{
		Key: "v2raytun-android", Label: "V2RayTun", Platform: "android",
		Official: "https://play.google.com/store/apps/details?id=com.v2raytun.android",
	},
	{
		Key: "npvtunnel-android", Label: "NPV Tunnel", Platform: "android",
		Official: "https://play.google.com/store/apps/details?id=com.napsternetlabs.napsternetv",
	},
	{
		Key: "shadowrocket", Label: "Shadowrocket", Platform: "ios",
		Official: "https://apps.apple.com/app/shadowrocket/id932747118",
	},
	{
		Key: "v2box-ios", Label: "V2Box", Platform: "ios",
		Official: "https://apps.apple.com/app/v2box-v2ray-client/id6446814690",
	},
	{
		Key: "v2raytun-ios", Label: "V2RayTun", Platform: "ios",
		Official: "https://apps.apple.com/app/v2raytun/id6476628951",
	},
	{
		Key: "npvtunnel-ios", Label: "NPV Tunnel", Platform: "ios",
		Official: "https://apps.apple.com/app/npv-tunnel/id1629465476",
	},
	{
		Key: "happ-ios", Label: "Happ", Platform: "ios",
		Official: "https://apps.apple.com/app/happ-proxy-utility/id6504287215",
	},
	{
		Key: "incy-ios", Label: "Incy", Platform: "ios",
		Official: "https://apps.apple.com/ru/app/incy/id6756943388",
	},
}

// clientDownloadItems resolves the catalog into absolute URLs for the page.
// base comes from the optional clientDownloadBase setting and lets an admin
// put the installers on a CDN or another host; leaving it empty keeps
// everything on this server.
func (a *SUBController) clientDownloadItems(base string) []clientDownload {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	items := make([]clientDownload, 0, len(clientCatalog))
	for _, entry := range clientCatalog {
		switch {
		case entry.File != "" && base != "":
			entry.URL = base + clientDownloadPath + entry.File
		case entry.File != "":
			entry.URL = clientDownloadPath + entry.File
		default:
			entry.URL = entry.Official
		}
		if entry.URL == "" {
			continue
		}
		items = append(items, entry)
	}
	return items
}

// clientsDir is where mirrored installers are read from.
func clientsDir() string {
	return filepath.Join(config.GetDBFolderPath(), "clients")
}

// serveClientFile streams a mirrored installer. Range requests are delegated
// to http.ServeFile so an interrupted multi-megabyte download can resume.
func (a *SUBController) serveClientFile(c *gin.Context) {
	name := c.Param("file")
	if !clientFilePattern.MatchString(name) {
		c.Status(http.StatusNotFound)
		return
	}
	fullPath := filepath.Join(clientsDir(), name)
	info, err := os.Stat(fullPath)
	if err != nil || info.IsDir() {
		logger.Debugf("sub: client installer %q not available: %v", name, err)
		c.Status(http.StatusNotFound)
		return
	}
	c.Header("Content-Type", "application/vnd.android.package-archive")
	c.Header("Content-Disposition", `attachment; filename="`+name+`"`)
	c.Header("Cache-Control", "public, max-age=86400")
	http.ServeFile(c.Writer, c.Request, fullPath)
}
