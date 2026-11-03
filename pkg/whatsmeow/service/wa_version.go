// Fork change (r4v3nz/evolution-go, branch fail-closed-proxy): this file is
// not part of upstream Evolution Go.
//
// WhatsApp refuses a login whose client version is too old ("Client outdated
// (405)"), and an instance that cannot log in never shows a QR code. Upstream
// wrote the chosen version (WHATSAPP_VERSION_* or the sw.js lookup) only into
// store.DeviceProps, which is not what WhatsApp checks at login: the login
// payload always carried the version compiled into whatsmeow. On 2026-11-03
// that version had become too old and sw.js answered HTTP 400, so every
// instance was refused. Here the chosen version also goes into the login
// payload (store.SetWAVersion), and the sw.js lookup can only move it forward.

package whatsmeow_service

import (
	"sync"

	"go.mau.fi/whatsmeow/store"
	"google.golang.org/protobuf/proto"
)

// builtInWAVersion is the version compiled into whatsmeow, read before
// anything replaces it.
var builtInWAVersion = store.GetWAVersion()

// waVersionMu serializes changes to whatsmeow's process-wide version.
var waVersionMu sync.Mutex

func (v clientVersion) container() store.WAVersionContainer {
	return store.WAVersionContainer{uint32(v.Major), uint32(v.Minor), uint32(v.Patch)}
}

// chooseWAVersion picks the version to log in with: WHATSAPP_VERSION_* when
// set (an explicit pin), else the WhatsApp Web version when it is newer than
// the built-in one, else the built-in one.
func chooseWAVersion(pinned, web *clientVersion) (store.WAVersionContainer, string) {
	if pinned != nil {
		return pinned.container(), "WHATSAPP_VERSION_*"
	}
	if web != nil && builtInWAVersion.LessThan(web.container()) {
		return web.container(), "WhatsApp Web"
	}
	return builtInWAVersion, "whatsmeow built-in"
}

// applyWAVersion puts v in the login payload and in the companion device
// props. Both are process-wide in whatsmeow, so every instance shares them.
func applyWAVersion(v store.WAVersionContainer) {
	waVersionMu.Lock()
	defer waVersionMu.Unlock()
	if store.GetWAVersion() != v {
		store.SetWAVersion(v)
	}
	store.DeviceProps.Version.Primary = proto.Uint32(v[0])
	store.DeviceProps.Version.Secondary = proto.Uint32(v[1])
	store.DeviceProps.Version.Tertiary = proto.Uint32(v[2])
}

// setClientVersion chooses and applies the version before an instance
// connects.
func (w whatsmeowService) setClientVersion(instanceID, proxyAddress string) {
	var pinned, web *clientVersion
	if w.config.WhatsappVersionMajor != 0 && w.config.WhatsappVersionMinor != 0 && w.config.WhatsappVersionPatch != 0 {
		pinned = &clientVersion{
			Major: w.config.WhatsappVersionMajor,
			Minor: w.config.WhatsappVersionMinor,
			Patch: w.config.WhatsappVersionPatch,
		}
	} else if v, err := fetchWhatsAppWebVersion(proxyAddress); err != nil {
		w.loggerWrapper.GetLogger(instanceID).LogWarn("[%s] Failed to fetch WhatsApp Web version, falling back to whatsmeow's built-in %s: %v", instanceID, builtInWAVersion, err)
	} else {
		web = v
	}

	v, source := chooseWAVersion(pinned, web)
	applyWAVersion(v)
	w.loggerWrapper.GetLogger(instanceID).LogInfo("[%s] WhatsApp client version %s (%s)", instanceID, v, source)
}
