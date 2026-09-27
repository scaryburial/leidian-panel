#!/usr/bin/env python3
"""Regression tests for the inbounds packaging/create-inbounds.py creates.

Run from the repository root:

    python3 packaging/test_create_inbounds.py

The script is a standalone installer, not a package, so it is loaded by path.
Its module-level code only builds an opener and reads env defaults; main() is
not called, so importing it touches neither the network nor the panel.
"""
import importlib.util
import json
import os
import sys
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))


def load_create_inbounds():
    path = os.path.join(HERE, "create-inbounds.py")
    spec = importlib.util.spec_from_file_location("create_inbounds", path)
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


ci = load_create_inbounds()

VISION = "xtls-rprx-vision"


def stream(network, security, **extra):
    return json.dumps(dict({"network": network, "security": security}, **extra))


def vless_client(settings):
    return json.loads(settings)["clients"][0]


class TestVisionFlowMatchesTransport(unittest.TestCase):
    """P1: only a transport that can carry XTLS Vision may declare the flow.

    create-inbounds.py used to stamp flow=xtls-rprx-vision on every VLESS client,
    including the WebSocket inbound on 2087. The panel's link/subscription
    builders strip the flow for a ws transport (vlessFlowAllowed in internal/sub),
    so the client never sends it while the server account still demands it and
    Xray rejects every connection:

        account <email> is rejected since the client flow is empty
    """

    def test_websocket_inbound_client_has_empty_flow(self):
        settings = ci.vless_settings("vless-ws", ci.ws("/ui3344ws"))
        client = vless_client(settings)
        self.assertEqual(
            client["flow"], "",
            "a ws inbound must not declare a flow: the client cannot send it and "
            "Xray rejects the connection",
        )

    def test_tcp_tls_and_reality_keep_vision(self):
        for stream_json in (
            stream("tcp", "tls", tlsSettings={}),
            stream("tcp", "reality", realitySettings={}),
        ):
            with self.subTest(stream=stream_json):
                client = vless_client(ci.vless_settings("vless-tls", stream_json))
                self.assertEqual(client["flow"], VISION)

    def test_every_other_transport_has_empty_flow(self):
        for network in ("ws", "httpupgrade", "grpc", "kcp", "quic", "http"):
            for security in ("none", "tls"):
                with self.subTest(network=network, security=security):
                    client = vless_client(
                        ci.vless_settings("x", stream(network, security))
                    )
                    self.assertEqual(client["flow"], "")

    def test_tcp_without_tls_has_empty_flow(self):
        client = vless_client(ci.vless_settings("vless-speed", ci.TCP_NONE))
        self.assertEqual(client["flow"], "")

    def test_vision_flow_for_matches_the_panel_gate(self):
        # Mirror of inboundCanEnableTlsFlow() in
        # internal/web/service/inbound_protocol.go: raw TCP over tls/reality only.
        cases = {
            ("tcp", "tls"): VISION,
            ("tcp", "reality"): VISION,
            ("tcp", "none"): "",
            ("ws", "none"): "",
            ("ws", "tls"): "",
            ("httpupgrade", "tls"): "",
            ("grpc", "tls"): "",
            ("kcp", "none"): "",
            ("quic", "none"): "",
            ("http", "none"): "",
        }
        for (network, security), want in cases.items():
            with self.subTest(network=network, security=security):
                self.assertEqual(
                    ci.vision_flow_for(stream(network, security)), want
                )

    def test_client_keeps_its_other_fields(self):
        client = vless_client(ci.vless_settings("vless-ws", ci.ws("/ui3344ws")))
        for field in ("id", "email", "subId"):
            self.assertTrue(client.get(field), "%s must still be generated" % field)
        self.assertTrue(client["enable"])
        self.assertEqual(client["email"], "vless-ws")
        self.assertEqual(len(client["subId"]), 16)


class TestAlpnNeverAdvertisesH2ToWebSocket(unittest.TestCase):
    """P5: WebSocket cannot run on HTTP/2, so h2 must not be offered.

    Measured on a real node: alpn=http/1.1 answered generate_204 with 204, while
    alpn=h2,http/1.1 answered 000 with `websocket: protocol "h2" was given but is
    not supported` / `malformed HTTP response "\\x00\\x00\\x12\\x04..."`. The Go
    side already writes http/1.1 only (internal/web/service/domain/domain.go).
    """

    def test_tls_stream_offers_only_http_1_1(self):
        tls = json.loads(ci.tls_selfsigned())["tlsSettings"]
        self.assertEqual(tls["alpn"], ["http/1.1"])
        self.assertNotIn("h2", tls["alpn"])

    def test_module_alpn_constant_has_no_h2(self):
        self.assertNotIn("h2", ci.TLS_ALPN)

    def test_tls_stream_still_carries_its_certificate(self):
        tls = json.loads(ci.tls_selfsigned())["tlsSettings"]
        cert = tls["certificates"][0]
        self.assertTrue(cert["certificateFile"].endswith("fullchain.pem"))
        self.assertTrue(cert["keyFile"].endswith("privkey.pem"))


class TestPresetInbounds(unittest.TestCase):
    """The assembled presets must carry the fixed settings end to end."""

    def build_items(self):
        # The same construction main() performs, minus the panel round-trip.
        items = []
        ws_stream = ci.ws("/ui3344ws")
        items.append(
            ci.inbound(
                "VLESS-综合-WS-2087",
                2087,
                "vless",
                ci.vless_settings("vless-ws", ws_stream),
                ws_stream,
            )
        )
        tls_stream = ci.tls_selfsigned()
        items.append(
            ci.inbound(
                "VMess-安全-TLS-2053",
                2053,
                "vmess",
                ci.vmess_settings("vmess-tls"),
                tls_stream,
            )
        )
        reality_stream = ci.reality("priv", "pub", "sid")
        items.append(
            ci.inbound(
                "VLESS-安全-Reality-443",
                443,
                "vless",
                ci.vless_settings("vless-reality", reality_stream),
                reality_stream,
            )
        )
        return items

    def test_ws_preset_and_reality_preset_agree_with_their_transport(self):
        items = {i["remark"]: i for i in self.build_items()}

        ws = items["VLESS-综合-WS-2087"]
        self.assertEqual(json.loads(ws["streamSettings"])["network"], "ws")
        self.assertEqual(json.loads(ws["settings"])["clients"][0]["flow"], "")

        reality = items["VLESS-安全-Reality-443"]
        self.assertEqual(json.loads(reality["streamSettings"])["network"], "tcp")
        self.assertEqual(
            json.loads(reality["settings"])["clients"][0]["flow"], VISION
        )

    def test_no_bound_stream_advertises_h2(self):
        for item in self.build_items():
            tls = json.loads(item["streamSettings"]).get("tlsSettings")
            if tls is None:
                continue
            with self.subTest(remark=item["remark"]):
                self.assertNotIn("h2", tls.get("alpn", []))


if __name__ == "__main__":
    unittest.main(verbosity=2)
