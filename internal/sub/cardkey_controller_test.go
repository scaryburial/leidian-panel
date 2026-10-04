package sub

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/cardkey"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

type cardKeyFixture struct {
	router    *gin.Engine
	subID     string
	cardKey   string // display form, as an operator would hand it out
	client    *model.ClientRecord
	inboundID int
}

// initCardKeySubRouter builds the subscription router the way the panel does
// (raw + JSON + Clash enabled) and seeds one enabled client that has both a
// subId and a card key. Encryption is off so failures show the actual links.
func initCardKeySubRouter(t *testing.T, options ...SUBControllerOption) *cardKeyFixture {
	t.Helper()
	tmp := t.TempDir()
	t.Chdir(tmp)
	if err := os.MkdirAll("internal/web/dist", 0o755); err != nil {
		t.Fatalf("mkdir dist: %v", err)
	}
	if err := os.WriteFile("internal/web/dist/subpage.html", []byte("<html><head></head><body></body></html>"), 0o644); err != nil {
		t.Fatalf("write subpage: %v", err)
	}
	t.Setenv("XUI_DB_FOLDER", tmp)
	if err := database.InitDB(filepath.Join(tmp, "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = database.CloseDB() })

	db := database.GetDB()
	inbound := &model.Inbound{
		UserId:         1,
		Tag:            "cardkey-sub",
		Enable:         true,
		Port:           443,
		Protocol:       model.VLESS,
		Settings:       `{"clients":[]}`,
		StreamSettings: `{"network":"tcp","security":"none"}`,
	}
	if err := db.Create(inbound).Error; err != nil {
		t.Fatalf("seed inbound: %v", err)
	}
	key := cardkey.Generate()
	client := &model.ClientRecord{
		Email:      "cardkey@example.com",
		SubID:      "sub-card-route",
		UUID:       "11111111-2222-4333-8444-555555555555",
		Enable:     true,
		TotalGB:    100 << 30,
		ExpiryTime: 0,
		CardKey:    &key,
	}
	if err := db.Create(client).Error; err != nil {
		t.Fatalf("seed client: %v", err)
	}
	if err := db.Create(&model.ClientInbound{ClientId: client.Id, InboundId: inbound.Id}).Error; err != nil {
		t.Fatalf("seed client inbound: %v", err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	base := []SUBControllerOption{
		WithSUBPath("/sub/"),
		WithSUBJsonPath("/json/"),
		WithSUBClashPath("/clash/"),
		WithSUBJsonEnabled(true),
		WithSUBClashEnabled(true),
		WithSUBEncryption(false),
	}
	NewSUBController(router.Group("/"), append(base, options...)...)
	return &cardKeyFixture{router: router, subID: client.SubID, cardKey: cardkey.Format(key), client: client, inboundID: inbound.Id}
}

func cardKeyRequest(t *testing.T, router *gin.Engine, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = "sub.example.com"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// profileWebPageURL is the one header that legitimately differs between the two
// URLs: it echoes the request URI back so a client can find its own page.
const profileWebPageURL = "Profile-Web-Page-Url"

func assertSameResponse(t *testing.T, label string, cardRec, subRec *httptest.ResponseRecorder) {
	t.Helper()
	if cardRec.Code != subRec.Code {
		t.Fatalf("%s: card key status = %d, subId status = %d, want identical (card body %q, sub body %q)",
			label, cardRec.Code, subRec.Code, cardRec.Body.String(), subRec.Body.String())
	}
	if cardRec.Body.String() != subRec.Body.String() {
		t.Fatalf("%s: card key body != subId body\n--- card key ---\n%s\n--- subId ---\n%s", label, cardRec.Body.String(), subRec.Body.String())
	}
	cardHeader := cardRec.Header().Clone()
	subHeader := subRec.Header().Clone()
	cardProfile := cardHeader.Get(profileWebPageURL)
	subProfile := subHeader.Get(profileWebPageURL)
	cardHeader.Del(profileWebPageURL)
	subHeader.Del(profileWebPageURL)
	if !reflect.DeepEqual(cardHeader, subHeader) {
		t.Fatalf("%s: headers differ\n--- card key ---\n%v\n--- subId ---\n%v", label, cardHeader, subHeader)
	}
	// The profile URL must still point at the URL the client actually used.
	if strings.Contains(subProfile, "/sub/") && !strings.Contains(cardProfile, "/c/") {
		t.Fatalf("%s: card key response advertised %q instead of a /c/ URL", label, cardProfile)
	}
}

// The whole requirement in one test: /c/<card_key> and /<subPath>/<subId> are
// the same response, for the raw, Clash and JSON branches.
func TestCardKeySubscriptionMatchesSubIdRoute(t *testing.T) {
	f := initCardKeySubRouter(t)

	pairs := []struct {
		name string
		sub  string
		card string
	}{
		{"raw", "/sub/" + f.subID, "/c/" + f.cardKey},
		{"clash", "/clash/" + f.subID, "/c/clash/" + f.cardKey},
		{"json", "/json/" + f.subID, "/c/json/" + f.cardKey},
	}
	for _, pair := range pairs {
		cardRec := cardKeyRequest(t, f.router, pair.card)
		subRec := cardKeyRequest(t, f.router, pair.sub)
		assertSameResponse(t, pair.name, cardRec, subRec)
		if cardRec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200 (body %q)", pair.name, cardRec.Code, cardRec.Body.String())
		}
		if strings.TrimSpace(cardRec.Body.String()) == "" {
			t.Fatalf("%s: empty body; the parity assertion would be vacuous", pair.name)
		}
	}

	// The Subscription-Userinfo header is what VPN clients read for quota and
	// expiry, so it must survive the card-key route.
	cardRec := cardKeyRequest(t, f.router, "/c/"+f.cardKey)
	userinfo := cardRec.Header().Get("Subscription-Userinfo")
	if !strings.Contains(userinfo, "total=") || !strings.Contains(userinfo, "upload=") {
		t.Fatalf("card key response is missing Subscription-Userinfo: %q (%v)", userinfo, cardRec.Header())
	}
	if !strings.Contains(cardRec.Body.String(), "vless://") {
		t.Fatalf("card key raw body does not contain a link: %q", cardRec.Body.String())
	}
}

// HEAD must behave like GET, as it does on the subId route.
func TestCardKeySubscriptionHeadMatchesGet(t *testing.T) {
	f := initCardKeySubRouter(t)
	for _, path := range []string{"/sub/" + f.subID, "/c/" + f.cardKey} {
		req := httptest.NewRequest(http.MethodHead, path, nil)
		req.Host = "sub.example.com"
		rec := httptest.NewRecorder()
		f.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("HEAD %s = %d, want 200", path, rec.Code)
		}
	}
}

// Customers mistype: every accepted spelling has to resolve to the same
// subscription.
func TestCardKeySubscriptionAcceptsEverySpelling(t *testing.T) {
	f := initCardKeySubRouter(t)
	want := cardKeyRequest(t, f.router, "/sub/"+f.subID)
	confused := strings.NewReplacer("0", "O", "1", "l").Replace(f.cardKey)
	for _, spelling := range []string{
		f.cardKey,
		strings.ToLower(f.cardKey),
		strings.ToLower(strings.ReplaceAll(f.cardKey, "-", "")),
		confused,
		strings.ReplaceAll(f.cardKey, "-", ""),
	} {
		rec := cardKeyRequest(t, f.router, "/c/"+spelling)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /c/%s = %d, want 200 (body %q)", spelling, rec.Code, rec.Body.String())
		}
		if rec.Body.String() != want.Body.String() {
			t.Fatalf("GET /c/%s body differs from the subId route:\n%s\n--- want ---\n%s", spelling, rec.Body.String(), want.Body.String())
		}
	}
}

// An unknown card key, a malformed card key and an unknown subId must be
// indistinguishable: same status, same (empty) body, same headers.
func TestCardKeySubscriptionUnknownKeyIsIndistinguishable(t *testing.T) {
	f := initCardKeySubRouter(t)
	unknownSub := cardKeyRequest(t, f.router, "/sub/does-not-exist")
	if unknownSub.Code != http.StatusNotFound {
		t.Fatalf("unknown subId status = %d, want 404", unknownSub.Code)
	}
	if unknownSub.Body.Len() != 0 {
		t.Fatalf("unknown subId body = %q, want empty", unknownSub.Body.String())
	}

	// "/c/" itself has no route (the wildcard needs a segment), so it is left
	// out: it is answered by the router, not by the card-key resolver.
	malformed := []string{"ABC", "LD-", f.cardKey + "Z", strings.Repeat("Z", 200), "0123456789AB"}
	// A well-formed key that no client owns: flip the check character.
	owner := cardkey.Normalize(f.cardKey)
	orphan := owner[:cardkey.DataLen] + string(cardkey.Alphabet[(strings.IndexByte(cardkey.Alphabet, owner[cardkey.DataLen])+1)%len(cardkey.Alphabet)])
	malformed = append(malformed, cardkey.Format(orphan))
	for _, key := range malformed {
		rec := cardKeyRequest(t, f.router, "/c/"+key)
		if rec.Code != unknownSub.Code {
			t.Fatalf("GET /c/%s = %d, want %d (same as an unknown subId)", key, rec.Code, unknownSub.Code)
		}
		if rec.Body.String() != unknownSub.Body.String() {
			t.Fatalf("GET /c/%s body = %q, want %q", key, rec.Body.String(), unknownSub.Body.String())
		}
		if !reflect.DeepEqual(rec.Header(), unknownSub.Header()) {
			t.Fatalf("GET /c/%s headers = %v, want %v (same as an unknown subId)", key, rec.Header(), unknownSub.Header())
		}
	}
}

// Disabled, expired and depleted clients must be served exactly as their subId
// route serves them — the card key must not invent a second policy.
func TestCardKeySubscriptionStateParity(t *testing.T) {
	f := initCardKeySubRouter(t)
	db := database.GetDB()

	cases := []struct {
		name  string
		apply func()
	}{
		{"disabled", func() {
			if err := db.Model(&model.ClientRecord{}).Where("id = ?", f.client.Id).
				UpdateColumn("enable", false).Error; err != nil {
				t.Fatalf("disable client: %v", err)
			}
		}},
		{"expired", func() {
			if err := db.Model(&model.ClientRecord{}).Where("id = ?", f.client.Id).
				Updates(map[string]any{"enable": true, "expiry_time": 1_600_000_000_000}).Error; err != nil {
				t.Fatalf("expire client: %v", err)
			}
		}},
		{"depleted", func() {
			if err := db.Model(&model.ClientRecord{}).Where("id = ?", f.client.Id).
				Updates(map[string]any{"enable": true, "expiry_time": 4102444800000}).Error; err != nil {
				t.Fatalf("restore client: %v", err)
			}
			if err := db.Create(&xray.ClientTraffic{
				InboundId: f.inboundID, Email: f.client.Email, Enable: true,
				Up: 60 << 30, Down: 60 << 30, Total: 100 << 30,
			}).Error; err != nil {
				t.Fatalf("seed traffic: %v", err)
			}
		}},
	}
	for _, tc := range cases {
		tc.apply()
		for _, path := range []struct{ name, sub, card string }{
			{"raw", "/sub/" + f.subID, "/c/" + f.cardKey},
			{"clash", "/clash/" + f.subID, "/c/clash/" + f.cardKey},
			{"json", "/json/" + f.subID, "/c/json/" + f.cardKey},
		} {
			assertSameResponse(t, tc.name+"/"+path.name,
				cardKeyRequest(t, f.router, path.card),
				cardKeyRequest(t, f.router, path.sub))
		}
	}
}

// The HWID device gate reads the resolved subscription, so a card-key request
// must consume and reject device slots exactly like the subId route.
func TestCardKeySubscriptionHwidGateApplies(t *testing.T) {
	f := initCardKeySubRouter(t)
	db := database.GetDB()
	if err := db.Model(&model.ClientRecord{}).Where("id = ?", f.client.Id).
		UpdateColumn("limit_hwid", 1).Error; err != nil {
		t.Fatalf("set hwid limit: %v", err)
	}

	cardRec := cardKeyRequest(t, f.router, "/c/"+f.cardKey)
	subRec := cardKeyRequest(t, f.router, "/sub/"+f.subID)
	assertSameResponse(t, "hwid/reject", cardRec, subRec)
	if cardRec.Code != http.StatusNotFound {
		t.Fatalf("card key without X-HWID = %d, want 404 like the subId route", cardRec.Code)
	}
	if cardRec.Header().Get("X-Hwid-Not-Supported") != "true" {
		t.Fatalf("card key rejection lost the HWID headers: %v", cardRec.Header())
	}

	// Register one device through the card-key route; the slot must be spent
	// on the resolved subscription, so the second device is refused on both.
	register := func(path string, hwid string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "sub.example.com"
		req.Header.Set("X-HWID", hwid)
		rec := httptest.NewRecorder()
		f.router.ServeHTTP(rec, req)
		return rec
	}
	if rec := register("/c/"+f.cardKey, "device-one"); rec.Code != http.StatusOK {
		t.Fatalf("card key with a first device = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if rec := register("/sub/"+f.subID, "device-two"); rec.Code != http.StatusNotFound {
		t.Fatalf("subId with a second device = %d, want 404: the card-key fetch must have spent the same slot", rec.Code)
	}
	assertSameResponse(t, "hwid/second-device",
		register("/c/"+f.cardKey, "device-three"),
		register("/sub/"+f.subID, "device-four"))

	// The read-only device-slot endpoint must answer for a card key too, and
	// with the same counters as the subId route.
	statusCard := cardKeyRequest(t, f.router, "/c/"+f.cardKey+"/hwid-status")
	statusSub := cardKeyRequest(t, f.router, "/sub/"+f.subID+"/hwid-status")
	assertSameResponse(t, "hwid/status", statusCard, statusSub)
	if statusCard.Code != http.StatusOK || !strings.Contains(statusCard.Body.String(), "limit") {
		t.Fatalf("hwid-status via card key = %d %q, want a 200 counter body", statusCard.Code, statusCard.Body.String())
	}
}

// A browser opening the card-key URL gets the same info page as the subId URL:
// the page is built from the resolved subscription, so nothing in it can drift.
func TestCardKeySubscriptionHtmlPageMatchesSubIdRoute(t *testing.T) {
	f := initCardKeySubRouter(t)
	page := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "sub.example.com"
		req.Header.Set("Accept", "text/html")
		rec := httptest.NewRecorder()
		f.router.ServeHTTP(rec, req)
		return rec
	}
	cardRec := page("/c/" + f.cardKey)
	subRec := page("/sub/" + f.subID)
	assertSameResponse(t, "html", cardRec, subRec)
	if cardRec.Code != http.StatusOK {
		t.Fatalf("card key HTML page = %d, want 200 (body %q)", cardRec.Code, cardRec.Body.String())
	}

	// ?format=info is the machine-readable variant of the same page.
	cardRec = cardKeyRequest(t, f.router, "/c/"+f.cardKey+"?format=info")
	subRec = cardKeyRequest(t, f.router, "/sub/"+f.subID+"?format=info")
	assertSameResponse(t, "info", cardRec, subRec)
	if cardRec.Code != http.StatusOK {
		t.Fatalf("card key format=info = %d, want 200 (body %q)", cardRec.Code, cardRec.Body.String())
	}
}

// The card-key path must not be registered when an operator already gave "/c/"
// to another subscription path: that would panic the router at startup.
func TestCardKeyRoutesSkipConflictingConfiguredPaths(t *testing.T) {
	configs := []struct {
		name         string
		options      []SUBControllerOption
		keyPathsWork bool
	}{
		{"raw owns /c/", []SUBControllerOption{WithSUBPath("/c/")}, false},
		{"clash owns /c/", []SUBControllerOption{WithSUBClashPath("/c/")}, false},
		{"json owns /c/", []SUBControllerOption{WithSUBJsonPath("/c/")}, false},
		{"free", nil, true},
	}
	for _, tc := range configs {
		t.Run(tc.name, func(t *testing.T) {
			// The constructor panics if the router tree conflicts, which is
			// exactly what this guards against.
			f := initCardKeySubRouter(t, tc.options...)
			rec := cardKeyRequest(t, f.router, "/c/"+f.cardKey)
			if tc.keyPathsWork {
				if rec.Code != http.StatusOK {
					t.Fatalf("GET /c/<card key> = %d, want 200 (body %q)", rec.Code, rec.Body.String())
				}
				return
			}
			// With "/c/" taken by another path, the card route is skipped: the
			// configured path serves the request instead, so the key is not
			// resolvable there and the answer is not a card-key answer.
			subRec := cardKeyRequest(t, f.router, "/c/"+f.subID)
			if subRec.Code != http.StatusOK {
				t.Fatalf("the configured path stopped working: /c/%s = %d", f.subID, subRec.Code)
			}
		})
	}
}
