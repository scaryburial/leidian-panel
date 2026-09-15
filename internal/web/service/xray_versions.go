package service

// bundledXrayVersions is the static list shown in the "switch Xray version"
// picker. It is baked in at build time so the panel never calls GitHub just to
// populate the list (privacy: no outbound request on opening the picker).
//
// Regenerate with:
//
//	curl -s "https://api.github.com/repos/XTLS/Xray-core/releases?per_page=40" \
//	  | grep '"tag_name"' | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/'
var bundledXrayVersions = []string{
	"v26.9.9",
	"v25.12.2",
	"v25.6.7",
}
