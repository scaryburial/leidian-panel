package service

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

const (
	visionFlowTest = "xtls-rprx-vision"

	tlsStreamInlineCert = `{"network":"tcp","security":"tls","tlsSettings":{` +
		`"certificates":[{"certificateFile":"/root/cert/ui3344-selfsigned/fullchain.pem",` +
		`"keyFile":"/root/cert/ui3344-selfsigned/privkey.pem","usage":"encipherment"}]}}`
	wsStreamTest = `{"network":"ws","security":"none","wsSettings":{"path":"/ui3344ws"}}`
)

// visionFlowClient is a VLESS client as packaging/create-inbounds.py used to
// write it: stamped with the XTLS Vision flow whatever transport the inbound
// actually uses.
func visionFlowClient(id, email, subID string) model.Client {
	return model.Client{ID: id, Email: email, SubID: subID, Enable: true, Flow: visionFlowTest}
}

// storedVisionFlow reads the flow back through both places the runtime config is
// built from: the inbound's settings blob (what the generated Xray file config
// carries) and the normalized clients/client_inbounds tables (what the live gRPC
// AlterInbound path and every share link/subscription read).
func storedVisionFlow(t *testing.T, svc *InboundService, id int, email string) (settingsFlow, overrideFlow string) {
	t.Helper()
	ib, err := svc.GetInbound(id)
	if err != nil {
		t.Fatalf("GetInbound(%d): %v", id, err)
	}
	settingsFlow = clientFlowsInSettings(t, ib.Settings)[email]

	list, err := svc.clientService.ListForInbound(nil, id)
	if err != nil {
		t.Fatalf("ListForInbound(%d): %v", id, err)
	}
	for _, c := range list {
		if c.Email == email {
			overrideFlow = c.Flow
		}
	}
	return settingsFlow, overrideFlow
}

// P1: a VLESS + WebSocket inbound created through the real AddInbound path must
// not keep the XTLS Vision flow. The panel's own link/subscription builders strip
// the flow for a ws transport (vlessFlowAllowed in internal/sub), so the client
// can never send it while the server account still demands it, and Xray refuses
// every connection with "account <email> is rejected since the client flow is
// empty". This is the regression create-inbounds.py shipped on port 2087.
func TestAddInbound_VisionFlowClearedOnWebSocketTransport(t *testing.T) {
	setupConflictDB(t)
	useTestRuntimeManager(t)
	svc := &InboundService{}

	const email = "vless-ws@x"
	inbound := &model.Inbound{
		Tag: "vless-ws-2087", Enable: true, Port: 52087, Protocol: model.VLESS,
		StreamSettings: wsStreamTest,
		Settings: clientsSettings(t, []model.Client{
			visionFlowClient("11111111-1111-1111-1111-111111111111", email, "sws1"),
		}),
	}
	if _, _, err := svc.AddInbound(inbound); err != nil {
		t.Fatalf("AddInbound: %v", err)
	}

	settingsFlow, overrideFlow := storedVisionFlow(t, svc, inbound.Id, email)
	if settingsFlow != "" {
		t.Errorf("settings flow = %q, want empty: a ws inbound whose account demands Vision rejects every client", settingsFlow)
	}
	if overrideFlow != "" {
		t.Errorf("client_inbounds.flow_override = %q, want empty: the live AlterInbound path and share links read this", overrideFlow)
	}

	// The generated runtime payload is what Xray's config is built from, so it
	// must carry the same empty flow — AddInbound persists and pushes one object.
	ib, err := svc.GetInbound(inbound.Id)
	if err != nil {
		t.Fatalf("GetInbound: %v", err)
	}
	runtimeInbound, err := svc.buildInboundForLocalRuntime(database.GetDB(), ib)
	if err != nil {
		t.Fatalf("buildInboundForLocalRuntime: %v", err)
	}
	if got := clientFlowsInSettings(t, runtimeInbound.Settings)[email]; got != "" {
		t.Errorf("runtime payload flow = %q, want empty", got)
	}
}

// The other half of the same gate: Vision IS valid on raw TCP carried over TLS
// or Reality, so the flow submitted for such an inbound must survive AddInbound
// untouched. Without this the sanitizer could pass by clearing flow everywhere.
func TestAddInbound_VisionFlowKeptOnEligibleTransports(t *testing.T) {
	cases := []struct {
		name   string
		stream string
		port   int
	}{
		{name: "tcp+tls", stream: tlsStreamInlineCert, port: 52443},
		{name: "tcp+reality", stream: realityStream, port: 52444},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupConflictDB(t)
			useTestRuntimeManager(t)
			svc := &InboundService{}

			email := "vless-eligible@x"
			inbound := &model.Inbound{
				Tag: "vless-" + tc.name, Enable: true, Port: tc.port, Protocol: model.VLESS,
				StreamSettings: tc.stream,
				Settings: clientsSettings(t, []model.Client{
					visionFlowClient("22222222-2222-2222-2222-222222222222", email, "sel1"),
				}),
			}
			if _, _, err := svc.AddInbound(inbound); err != nil {
				t.Fatalf("AddInbound: %v", err)
			}

			settingsFlow, overrideFlow := storedVisionFlow(t, svc, inbound.Id, email)
			if settingsFlow != visionFlowTest {
				t.Errorf("settings flow = %q, want %q kept on an eligible transport", settingsFlow, visionFlowTest)
			}
			if overrideFlow != visionFlowTest {
				t.Errorf("client_inbounds.flow_override = %q, want %q kept", overrideFlow, visionFlowTest)
			}
		})
	}
}

// A transport that only fails the eligibility gate until it is configured —
// VLESS on XHTTP before its vlessenc encryption, or on TCP before TLS is turned
// on — must NOT be sanitized: the flow is correct intent that comes back to life
// on the next edit. Only the transports Vision can never ride are stripped.
func TestRestoreVisionFlow_NonEligibleTransportSplit(t *testing.T) {
	initFlowTestDB(t)
	svc := &InboundService{}

	target := `{"clients":[{"id":"u1","email":"later@x","flow":"` + visionFlowTest + `","subId":"s1","enable":true}]}`

	// TCP without TLS and XHTTP without vlessenc: leave the flow alone.
	for _, stream := range []string{`{"network":"tcp","security":"none"}`, `{"network":"xhttp","security":"reality"}`} {
		out, changed := svc.restoreVisionFlowForEligibleInbound(nil, target, stream, model.VLESS)
		if changed {
			t.Errorf("stream %s: changed = true, want false (flow must survive until the transport is configured)", stream)
		}
		if got := clientFlowsInSettings(t, out)["later@x"]; got != visionFlowTest {
			t.Errorf("stream %s: flow = %q, want %q untouched", stream, got, visionFlowTest)
		}
	}

	// Transports Vision can never ride: strip it, and report the change so the
	// caller persists the rewritten settings.
	for _, stream := range []string{
		`{"network":"ws","security":"none"}`,
		`{"network":"ws","security":"tls"}`,
		`{"network":"httpupgrade","security":"none"}`,
		`{"network":"grpc","security":"tls"}`,
		`{"network":"kcp","security":"none"}`,
		`{"network":"quic","security":"none"}`,
		`{"network":"http","security":"none"}`,
	} {
		out, changed := svc.restoreVisionFlowForEligibleInbound(nil, target, stream, model.VLESS)
		if !changed {
			t.Errorf("stream %s: changed = false, want true (dead Vision flow must be deleted)", stream)
			continue
		}
		if got := clientFlowsInSettings(t, out)["later@x"]; got != "" {
			t.Errorf("stream %s: flow = %q, want empty", stream, got)
		}
	}

	// A non-VLESS inbound is never touched, whatever the transport says.
	if _, changed := svc.restoreVisionFlowForEligibleInbound(nil, target, wsStreamTest, model.VMESS); changed {
		t.Error("non-VLESS inbound must not be rewritten")
	}
}

// The transport predicate itself must agree with inboundCanEnableTlsFlow's
// rejection list, so a stored flow is only ever deleted where the verdict cannot
// be reversed by a later edit of the same inbound.
func TestTransportCanNeverUseVisionFlow(t *testing.T) {
	cases := map[string]bool{
		`{"network":"ws"}`:          true,
		`{"network":"httpupgrade"}`: true,
		`{"network":"grpc"}`:        true,
		`{"network":"kcp"}`:         true,
		`{"network":"quic"}`:        true,
		`{"network":"http"}`:        true,
		`{"network":"tcp"}`:         false,
		`{"network":"xhttp"}`:       false,
		`{"network":""}`:            false,
		``:                          false,
		`{"security":"tls"}`:        false,
		`not json`:                  false,
	}
	for stream, want := range cases {
		if got := transportCanNeverUseVisionFlow(stream); got != want {
			t.Errorf("transportCanNeverUseVisionFlow(%q) = %v, want %v", stream, got, want)
		}
	}

	// Every transport the eligibility gate rejects forever must be in the strip
	// list...
	for _, stream := range []string{
		`{"network":"ws","security":"none"}`,
		`{"network":"ws","security":"tls"}`,
		`{"network":"httpupgrade","security":"tls"}`,
		`{"network":"grpc","security":"tls"}`,
		`{"network":"kcp","security":"none"}`,
		`{"network":"quic","security":"none"}`,
		`{"network":"http","security":"tls"}`,
	} {
		if inboundCanEnableTlsFlow("vless", stream, `{"decryption":"none"}`) {
			t.Errorf("stream %s unexpectedly eligible; the strip list is wrong", stream)
		}
		if !transportCanNeverUseVisionFlow(stream) {
			t.Errorf("stream %s is ineligible yet not marked never-Vision", stream)
		}
	}
	// ...while TCP/XHTTP without their enabler must stay OUT of it, or turning on
	// TLS/vlessenc later would find the intent already erased.
	for _, stream := range []string{`{"network":"tcp","security":"none"}`, `{"network":"xhttp","security":"none"}`} {
		if inboundCanEnableTlsFlow("vless", stream, `{"decryption":"none"}`) {
			t.Errorf("stream %s should be ineligible before its enabler is set", stream)
		}
		if transportCanNeverUseVisionFlow(stream) {
			t.Errorf("stream %s is not yet eligible but IS fixable; stripping it would lose intent", stream)
		}
	}
}

// UpdateInbound is the self-healing path for an install that already stored the
// bad preset: editing the ws inbound must delete the flow instead of leaving it
// for the next subscription to fail against.
func TestUpdateInbound_ClearsVisionFlowOnWebSocketAndKeepsEligibleSibling(t *testing.T) {
	setupConflictDB(t)
	useTestRuntimeManager(t)
	svc := &InboundService{}

	// Both created through the real AddInbound path, so the fixture is exactly
	// the broken shipped state: settings AND client_inbounds carry Vision on a
	// transport that cannot express it.
	const wsEmail = "ws-shared@x"
	const realityEmail = "reality-shared@x"
	wsInbound := &model.Inbound{
		Tag: "vision-ws", Enable: true, Port: 52901, Protocol: model.VLESS,
		StreamSettings: wsStreamTest,
		Settings: clientsSettings(t, []model.Client{
			visionFlowClient("33333333-3333-3333-3333-333333333333", wsEmail, "sws9"),
		}),
	}
	realityInbound := &model.Inbound{
		Tag: "vision-reality", Enable: true, Port: 52902, Protocol: model.VLESS,
		StreamSettings: realityStream,
		Settings: clientsSettings(t, []model.Client{
			visionFlowClient("66666666-6666-6666-6666-666666666666", realityEmail, "srel9"),
		}),
	}
	if _, _, err := svc.AddInbound(wsInbound); err != nil {
		t.Fatalf("AddInbound(ws): %v", err)
	}
	if _, _, err := svc.AddInbound(realityInbound); err != nil {
		t.Fatalf("AddInbound(reality): %v", err)
	}

	// AddInbound already cleaned the ws one; put the broken state back so the
	// edit path is what this test actually exercises.
	broken, err := svc.GetInbound(wsInbound.Id)
	if err != nil {
		t.Fatalf("GetInbound(ws): %v", err)
	}
	broken.Settings = clientsSettings(t, []model.Client{
		visionFlowClient("33333333-3333-3333-3333-333333333333", wsEmail, "sws9"),
	})
	if err := database.GetDB().Model(&model.Inbound{}).
		Where("id = ?", broken.Id).Update("settings", broken.Settings).Error; err != nil {
		t.Fatalf("restore broken ws settings: %v", err)
	}
	if err := svc.clientService.SyncInbound(database.GetDB(), broken.Id, []model.Client{
		visionFlowClient("33333333-3333-3333-3333-333333333333", wsEmail, "sws9"),
	}); err != nil {
		t.Fatalf("restore broken ws link: %v", err)
	}

	// Fixture check: the broken state really is stored in both places.
	if f, o := storedVisionFlow(t, svc, wsInbound.Id, wsEmail); f != visionFlowTest || o != visionFlowTest {
		t.Fatalf("fixture: ws flow = (%q, %q), want (%q, %q)", f, o, visionFlowTest, visionFlowTest)
	}
	if f, o := storedVisionFlow(t, svc, realityInbound.Id, realityEmail); f != visionFlowTest || o != visionFlowTest {
		t.Fatalf("fixture: reality flow = (%q, %q), want (%q, %q)", f, o, visionFlowTest, visionFlowTest)
	}

	edit, err := svc.GetInbound(wsInbound.Id)
	if err != nil {
		t.Fatalf("GetInbound(ws) for edit: %v", err)
	}
	if _, _, err := svc.UpdateInbound(edit); err != nil {
		t.Fatalf("UpdateInbound(ws): %v", err)
	}

	settingsFlow, overrideFlow := storedVisionFlow(t, svc, wsInbound.Id, wsEmail)
	if settingsFlow != "" {
		t.Errorf("ws settings flow = %q, want empty after an edit", settingsFlow)
	}
	if overrideFlow != "" {
		t.Errorf("ws flow_override = %q, want empty after an edit", overrideFlow)
	}

	// The eligible sibling is untouched: editing one inbound must not disarm
	// Vision where it is the only transport that can carry it.
	siblingSettings, siblingOverride := storedVisionFlow(t, svc, realityInbound.Id, realityEmail)
	if siblingSettings != visionFlowTest {
		t.Errorf("reality settings flow = %q, want %q preserved", siblingSettings, visionFlowTest)
	}
	if siblingOverride != visionFlowTest {
		t.Errorf("reality flow_override = %q, want %q preserved", siblingOverride, visionFlowTest)
	}
}

// Regression guard for the enabling direction: an XHTTP inbound that becomes
// flow-eligible on edit must still get Vision back. Without this the transport
// sanitizer could silently kill the restore behaviour it shares a function with.
func TestUpdateInbound_RestoresVisionFlowWhenXhttpBecomesEligible(t *testing.T) {
	setupConflictDB(t)
	useTestRuntimeManager(t)
	svc := &InboundService{}

	const email = "xhttp@x"
	const id = "77777777-7777-7777-7777-777777777777"
	// The client's intended flow lives on an eligible reality sibling, so it is
	// resolvable through EffectiveFlowsByEmails.
	sibling := &model.Inbound{
		Tag: "xhttp-sibling", Enable: true, Port: 52921, Protocol: model.VLESS,
		StreamSettings: realityStream,
		Settings: clientsSettings(t, []model.Client{
			visionFlowClient(id, email, "sxs1"),
		}),
	}
	if _, _, err := svc.AddInbound(sibling); err != nil {
		t.Fatalf("AddInbound(sibling): %v", err)
	}

	// XHTTP without vlessenc: the same client, flow stripped because the
	// transport is not yet eligible.
	xhttp := &model.Inbound{
		Tag: "xhttp-target", Enable: true, Port: 52922, Protocol: model.VLESS,
		StreamSettings: `{"network":"xhttp","security":"reality"}`,
		Settings: clientsSettings(t, []model.Client{
			{ID: id, Email: email, SubID: "sxs1", Enable: true, Flow: ""},
		}),
	}
	if _, _, err := svc.AddInbound(xhttp); err != nil {
		t.Fatalf("AddInbound(xhttp): %v", err)
	}
	if f, o := storedVisionFlow(t, svc, xhttp.Id, email); f != "" || o != "" {
		t.Fatalf("fixture: xhttp flow = (%q, %q), want both empty before vlessenc", f, o)
	}

	// Enable VLESS encryption: the inbound is now Vision-eligible and the flow
	// must come back from the sibling's intent.
	edit, err := svc.GetInbound(xhttp.Id)
	if err != nil {
		t.Fatalf("GetInbound(xhttp): %v", err)
	}
	edit.Settings = `{"decryption":"mlkem768x25519plus.native.0rtt.KEY",` +
		`"encryption":"mlkem768x25519plus.native.0rtt.KEY","clients":[` +
		`{"id":"` + id + `","email":"` + email + `","flow":"","subId":"sxs1","enable":true}]}`
	if _, _, err := svc.UpdateInbound(edit); err != nil {
		t.Fatalf("UpdateInbound(xhttp): %v", err)
	}

	settingsFlow, overrideFlow := storedVisionFlow(t, svc, xhttp.Id, email)
	if settingsFlow != visionFlowTest {
		t.Errorf("settings flow = %q, want %q restored once vlessenc made xhttp eligible", settingsFlow, visionFlowTest)
	}
	if overrideFlow != visionFlowTest {
		t.Errorf("flow_override = %q, want %q restored", overrideFlow, visionFlowTest)
	}
}

// The boot migration must heal an already-installed panel too, not only edits.
func TestMigrationRestoreVisionFlow_HealsStoredWebSocketPreset(t *testing.T) {
	setupConflictDB(t)
	useTestRuntimeManager(t)
	db := database.GetDB()
	svc := &InboundService{}

	const email = "old-install@x"
	seedInboundConflict(t, "legacy-ws", "0.0.0.0", 52911, model.VLESS, wsStreamTest,
		clientsSettings(t, []model.Client{
			visionFlowClient("44444444-4444-4444-4444-444444444444", email, "sold1"),
		}))

	var ib model.Inbound
	if err := db.First(&ib, "tag = ?", "legacy-ws").Error; err != nil {
		t.Fatalf("load inbound: %v", err)
	}
	svc.MigrationRestoreVisionFlow()

	settingsFlow, overrideFlow := storedVisionFlow(t, svc, ib.Id, email)
	if settingsFlow != "" {
		t.Errorf("settings flow = %q, want empty after the boot migration", settingsFlow)
	}
	if overrideFlow != "" {
		t.Errorf("flow_override = %q, want empty after the boot migration", overrideFlow)
	}
}

// Guard the fixture itself: the settings JSON must actually carry the flow, so a
// passing sanitizer assertion cannot be an artefact of a client that never had one.
func TestVisionFlowFixtureCarriesTheFlow(t *testing.T) {
	settings := clientsSettings(t, []model.Client{
		visionFlowClient("55555555-5555-5555-5555-555555555555", "fixture@x", "sfx1"),
	})
	if got := clientFlowsInSettings(t, settings)["fixture@x"]; got != visionFlowTest {
		t.Fatalf("fixture flow = %q, want %q", got, visionFlowTest)
	}
}
