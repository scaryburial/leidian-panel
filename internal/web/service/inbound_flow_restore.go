package service

import (
	"encoding/json"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"

	"gorm.io/gorm"
)

const visionFlow = "xtls-rprx-vision"

// restoreVisionFlowForEligibleInbound re-gates the clients of a VLESS inbound
// against the inbound's now-final stream settings, in both directions.
//
// clientWithInboundFlow strips Vision from a client whenever the target inbound
// is not flow-eligible at write time (e.g. an XHTTP inbound before its vlessenc
// encryption is set). Nothing restored the flow when the inbound later became
// eligible — an inbound edit stores its settings verbatim and never re-gates the
// clients — so enabling encryption on an existing XHTTP inbound left every
// client without flow, and the share links/subscriptions dropped it.
//
//   - Eligible (TCP over TLS/Reality, or XHTTP with VLESS encryption): set
//     flow=Vision on each client that currently has no flow but whose intended
//     flow (its flow_override on a sibling inbound, via EffectiveFlowsByEmails)
//     is Vision. It never invents a flow for a client that has none anywhere,
//     and it never overwrites an explicit non-empty flow.
//
//   - Permanently ineligible (a transport Vision cannot ride: WebSocket,
//     HTTPUpgrade, gRPC, mKCP, QUIC, plain HTTP): delete the flow the inbound
//     still carries. Such a flow can only have come from a bulk client edit or a
//     pre-existing install — create-inbounds.py used to stamp Vision onto every
//     VLESS client, including the WebSocket one — and it makes the inbound
//     100% unusable: the panel's own link/subscription builders strip the flow
//     for these transports (vlessFlowAllowed in internal/sub), so the client
//     never sends it while the server account still demands it, and Xray
//     rejects the connection at handshake time with
//
//     account <email> is rejected since the client flow is empty
//
//     Deleting it here is what lets an already-installed panel heal on its next
//     UpdateInbound edit, MigrationRestoreVisionFlow run, or client edit.
//
// Returns the rewritten settings JSON and whether anything changed.
func (s *InboundService) restoreVisionFlowForEligibleInbound(tx *gorm.DB, settings, streamSettings string, protocol model.Protocol) (string, bool) {
	if protocol != model.VLESS {
		return settings, false
	}
	if !inboundCanEnableTlsFlow(string(protocol), streamSettings, settings) {
		if !transportCanNeverUseVisionFlow(streamSettings) {
			return settings, false
		}
		// No query needed: a transport that can never carry Vision has no flow
		// worth resolving intent for, so every stored flow is dead weight.
		return stripClientFlows(settings)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(settings), &parsed); err != nil {
		return settings, false
	}
	clients, ok := parsed["clients"].([]any)
	if !ok || len(clients) == 0 {
		return settings, false
	}
	// Collect empty-flow clients, then resolve their intended flow in one query.
	emails := make([]string, 0, len(clients))
	for i := range clients {
		cm, ok := clients[i].(map[string]any)
		if !ok {
			continue
		}
		if flow, _ := cm["flow"].(string); flow != "" {
			continue // respect an explicit flow (Vision or otherwise)
		}
		if email, _ := cm["email"].(string); email != "" {
			emails = append(emails, email)
		}
	}
	if len(emails) == 0 {
		return settings, false
	}
	intended, err := s.clientService.EffectiveFlowsByEmails(tx, emails)
	if err != nil {
		return settings, false
	}
	changed := false
	for i := range clients {
		cm, ok := clients[i].(map[string]any)
		if !ok {
			continue
		}
		if flow, _ := cm["flow"].(string); flow != "" {
			continue
		}
		email, _ := cm["email"].(string)
		if intended[email] != visionFlow {
			continue
		}
		cm["flow"] = visionFlow
		clients[i] = cm
		changed = true
	}
	if !changed {
		return settings, false
	}
	out, err := json.MarshalIndent(parsed, "", "  ")
	if err != nil {
		return settings, false
	}
	return string(out), true
}

// sanitizeVisionFlowForTransport applies restoreVisionFlowForEligibleInbound's
// ineligible-transport half to an inbound that is being created, so a client
// submitted with a flow the transport cannot express is never persisted with it
// (and never reaches the generated Xray config). The clients slice is rewritten
// in place, because the callers hand that same slice to ClientService.SyncInbound
// and it is what lands in clients/client_inbounds — normalizing only the settings
// JSON would leave the flow alive in the normalized tables.
//
// Unlike the update path this ignores tx: a brand-new inbound has no sibling
// flow_override rows to consult, so no eligibility query is needed.
func (s *InboundService) sanitizeVisionFlowForTransport(inbound *model.Inbound, clients []model.Client) {
	if inbound.DisableFlow || inbound.Protocol != model.VLESS {
		return
	}
	if !transportCanNeverUseVisionFlow(inbound.StreamSettings) {
		return
	}
	if stripped, changed := stripClientFlows(inbound.Settings); changed {
		inbound.Settings = stripped
	}
	for i := range clients {
		clients[i].Flow = ""
	}
}

func stripClientFlows(settings string) (string, bool) {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(settings), &parsed); err != nil {
		return settings, false
	}
	clients, ok := parsed["clients"].([]any)
	if !ok || len(clients) == 0 {
		return settings, false
	}
	changed := false
	for i := range clients {
		cm, ok := clients[i].(map[string]any)
		if !ok {
			continue
		}
		if flow, _ := cm["flow"].(string); flow != "" {
			cm["flow"] = ""
			clients[i] = cm
			changed = true
		}
	}
	if !changed {
		return settings, false
	}
	out, err := json.MarshalIndent(parsed, "", "  ")
	if err != nil {
		return settings, false
	}
	return string(out), true
}
