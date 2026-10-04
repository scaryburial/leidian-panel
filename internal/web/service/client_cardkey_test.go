package service

import (
	"bytes"
	"encoding/csv"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/cardkey"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"

	"gorm.io/gorm"
)

func cardKeyPtr(s string) *string { return &s }

// seedCardKeyInbound creates the kind of inbound a card-key batch is generated
// onto: a plain, enabled VLESS inbound with no clients yet.
func seedCardKeyInbound(t *testing.T, db *gorm.DB, port int) *model.Inbound {
	t.Helper()
	inbound := &model.Inbound{
		UserId:         1,
		Tag:            "cardkey-test",
		Enable:         true,
		Port:           port,
		Protocol:       model.VLESS,
		Settings:       `{"clients":[]}`,
		StreamSettings: `{"network":"tcp","security":"none"}`,
	}
	if err := db.Create(inbound).Error; err != nil {
		t.Fatalf("create inbound: %v", err)
	}
	return inbound
}

// clients.card_key must be unique but optional: the panel already has thousands
// of clients without a key, and every later create has to keep working.
func TestClientCardKeyColumnIsOptionalAndUnique(t *testing.T) {
	db := initTrafficTestDB(t)

	if !db.Migrator().HasColumn(&model.ClientRecord{}, "card_key") {
		t.Fatal("clients.card_key column was not created by AutoMigrate")
	}
	if !db.Migrator().HasIndex(&model.ClientRecord{}, "idx_clients_card_key") {
		t.Fatal("clients.card_key unique index was not created by AutoMigrate")
	}

	// Many keyless clients must coexist: NULL is distinct in a unique index,
	// "" is not.
	for _, email := range []string{"keyless-a@x", "keyless-b@x", "keyless-c@x"} {
		if err := db.Create(&model.ClientRecord{Email: email, Enable: true}).Error; err != nil {
			t.Fatalf("create keyless client %q: %v (a unique index over empty strings would refuse the second one)", email, err)
		}
	}
	var keyless int64
	if err := db.Model(&model.ClientRecord{}).Where("card_key IS NULL").Count(&keyless).Error; err != nil {
		t.Fatalf("count keyless clients: %v", err)
	}
	if keyless != 3 {
		t.Fatalf("keyless clients stored as NULL = %d, want 3", keyless)
	}

	key := cardkey.Generate()
	if err := db.Create(&model.ClientRecord{Email: "keyed@x", Enable: true, CardKey: cardKeyPtr(key)}).Error; err != nil {
		t.Fatalf("store card key: %v", err)
	}
	err := db.Create(&model.ClientRecord{Email: "duplicate@x", Enable: true, CardKey: cardKeyPtr(key)}).Error
	if err == nil {
		t.Fatal("a duplicate card_key was accepted; the unique index is missing")
	}
	if !isCardKeyUniqueViolation(err) {
		t.Fatalf("duplicate card_key error = %v, want a unique violation", err)
	}
}

func TestGetSubIDByCardKeyAcceptsEverySpelling(t *testing.T) {
	db := initTrafficTestDB(t)
	key := cardkey.Generate()
	rec := &model.ClientRecord{Email: "spelling@x", SubID: "sub-spelling", Enable: true, CardKey: cardKeyPtr(key)}
	if err := db.Create(rec).Error; err != nil {
		t.Fatalf("seed client: %v", err)
	}
	svc := &ClientService{}

	confused := strings.NewReplacer("0", "O", "1", "l").Replace(key)
	for _, spelling := range []string{key, cardkey.Format(key), strings.ToLower(key), strings.ToLower(cardkey.Format(key)), confused} {
		subID, found, err := svc.GetSubIDByCardKey(spelling)
		if err != nil || !found || subID != "sub-spelling" {
			t.Errorf("GetSubIDByCardKey(%q) = %q, %v, %v; want sub-spelling, true, nil", spelling, subID, found, err)
		}
	}

	// A key that is well formed but belongs to nobody, and a malformed key,
	// must both be reported as "not found" without an error: the caller turns
	// both into the same 404.
	for _, missing := range []string{cardkey.Generate(), "", "ABC", key + "Z", strings.Repeat("Z", 200)} {
		subID, found, err := svc.GetSubIDByCardKey(missing)
		if found || subID != "" || err != nil {
			t.Errorf("GetSubIDByCardKey(%q) = %q, %v, %v; want empty, false, nil", missing, subID, found, err)
		}
	}

	// A client whose stored sub_id is empty has no subscription to serve.
	empty := &model.ClientRecord{Email: "nosub@x", SubID: "", Enable: true, CardKey: cardKeyPtr(cardkey.Generate())}
	if err := db.Create(empty).Error; err != nil {
		t.Fatalf("seed sub-less client: %v", err)
	}
	if subID, found, err := svc.GetSubIDByCardKey(*empty.CardKey); found || subID != "" || err != nil {
		t.Errorf("GetSubIDByCardKey(sub-less client) = %q, %v, %v; want empty, false, nil", subID, found, err)
	}
}

func TestCardKeyLookupReportsUnknownKeys(t *testing.T) {
	db := initTrafficTestDB(t)
	key := cardkey.Generate()
	if err := db.Create(&model.ClientRecord{Email: "lookup@x", SubID: "sub-lookup", Enable: true, CardKey: cardKeyPtr(key)}).Error; err != nil {
		t.Fatalf("seed client: %v", err)
	}
	svc := &ClientService{}

	rec, err := svc.CardKeyLookup(cardkey.Format(key))
	if err != nil {
		t.Fatalf("CardKeyLookup(%q): %v", cardkey.Format(key), err)
	}
	if rec.Email != "lookup@x" {
		t.Fatalf("CardKeyLookup returned %q, want lookup@x", rec.Email)
	}
	if _, err := svc.CardKeyLookup(cardkey.Generate()); err == nil {
		t.Fatal("CardKeyLookup accepted a key that belongs to nobody")
	}
	if _, err := svc.CardKeyLookup("not-a-key"); err == nil {
		t.Fatal("CardKeyLookup accepted a malformed key")
	}
}

func TestGenerateCardKeysCreatesClientsOnTheInbound(t *testing.T) {
	db := initTrafficTestDB(t)
	inbound := seedCardKeyInbound(t, db, 8443)
	svc := &ClientService{}
	inboundSvc := &InboundService{}

	const totalGB = 5 << 30
	before := time.Now().UnixMilli()
	result, needRestart, err := svc.GenerateCardKeys(inboundSvc, CardKeyGenerateParams{
		InboundID:    inbound.Id,
		Count:        3,
		TotalGB:      totalGB,
		ExpiryDays:   30,
		RemarkPrefix: "tg",
		GroupName:    "vip",
	})
	if err != nil {
		t.Fatalf("GenerateCardKeys: %v", err)
	}
	_ = needRestart
	if result.Created != 3 || result.Keyed != 3 || len(result.Keys) != 3 || len(result.Skipped) != 0 {
		t.Fatalf("result = created %d, keyed %d, keys %d, skipped %v; want 3/3/3/none",
			result.Created, result.Keyed, len(result.Keys), result.Skipped)
	}

	seen := map[string]struct{}{}
	wantEmails := []string{"tg0001", "tg0002", "tg0003"}
	for i, entry := range result.Keys {
		if !cardkey.IsWellFormed(entry.CardKey) {
			t.Fatalf("key %d = %q is not well formed", i, entry.CardKey)
		}
		if !strings.HasPrefix(entry.CardKey, cardkey.Prefix) {
			t.Fatalf("key %d = %q is not in display form", i, entry.CardKey)
		}
		if _, dup := seen[entry.CardKey]; dup {
			t.Fatalf("key %d = %q was handed out twice in one batch", i, entry.CardKey)
		}
		seen[entry.CardKey] = struct{}{}
		if entry.Email != wantEmails[i] {
			t.Fatalf("key %d email = %q, want %q", i, entry.Email, wantEmails[i])
		}
		if entry.InboundID != inbound.Id || entry.TotalGB != totalGB || !entry.Enable {
			t.Fatalf("key %d entry = %+v, want inbound %d, total %d, enabled", i, entry, inbound.Id, totalGB)
		}
	}

	var records []model.ClientRecord
	if err := db.Order("id ASC").Find(&records).Error; err != nil {
		t.Fatalf("load clients: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("clients rows = %d, want 3", len(records))
	}
	for i, rec := range records {
		entry := result.Keys[i]
		if rec.Email != entry.Email {
			t.Fatalf("row %d email = %q, want %q", i, rec.Email, entry.Email)
		}
		if rec.SubID != entry.SubID {
			t.Fatalf("row %d sub_id = %q, want the reported %q", i, rec.SubID, entry.SubID)
		}
		if rec.CardKey == nil || *rec.CardKey != cardkey.Normalize(entry.CardKey) {
			t.Fatalf("row %d card_key = %v, want the canonical %q", i, rec.CardKey, cardkey.Normalize(entry.CardKey))
		}
		if rec.TotalGB != totalGB || rec.Group != "vip" || rec.Comment != "tg" || !rec.Enable {
			t.Fatalf("row %d = %+v, want totalGB %d, group vip, comment tg, enabled", i, rec, totalGB)
		}
		if rec.ExpiryTime < before+29*24*3600*1000 || rec.ExpiryTime > time.Now().UnixMilli()+30*24*3600*1000 {
			t.Fatalf("row %d expiry = %d, want about 30 days from now", i, rec.ExpiryTime)
		}
		var links int64
		if err := db.Model(&model.ClientInbound{}).Where("client_id = ? AND inbound_id = ?", rec.Id, inbound.Id).Count(&links).Error; err != nil {
			t.Fatalf("count client_inbounds: %v", err)
		}
		if links != 1 {
			t.Fatalf("row %d is attached to the inbound %d times, want 1", i, links)
		}
	}

	// The keys must resolve back to the subscription they were generated for.
	for _, entry := range result.Keys {
		subID, found, err := svc.GetSubIDByCardKey(entry.CardKey)
		if err != nil || !found || subID != entry.SubID {
			t.Fatalf("GetSubIDByCardKey(%q) = %q, %v, %v; want %q, true, nil", entry.CardKey, subID, found, err, entry.SubID)
		}
	}

	// The inbound's settings must list the new clients, or xray would never
	// see them.
	stored, err := (&InboundService{}).GetInbound(inbound.Id)
	if err != nil {
		t.Fatalf("reload inbound: %v", err)
	}
	for _, entry := range result.Keys {
		if !strings.Contains(stored.Settings, entry.Email) {
			t.Fatalf("inbound settings do not mention %q: %s", entry.Email, stored.Settings)
		}
	}

	// A second batch on the same prefix must not collide with the first.
	second, _, err := svc.GenerateCardKeys(inboundSvc, CardKeyGenerateParams{
		InboundID:    inbound.Id,
		Count:        2,
		RemarkPrefix: "tg",
	})
	if err != nil {
		t.Fatalf("second GenerateCardKeys: %v", err)
	}
	if second.Created != 2 || second.Keys[0].Email != "tg0004" || second.Keys[1].Email != "tg0005" {
		t.Fatalf("second batch = %+v, want tg0004 and tg0005", second.Keys)
	}
}

// A generate call that starts with a prefix already partly in use must fill the
// gaps instead of failing on the first taken address.
func TestGenerateCardKeysSkipsTakenSequenceNumbers(t *testing.T) {
	db := initTrafficTestDB(t)
	inbound := seedCardKeyInbound(t, db, 8443)
	for _, email := range []string{"c0001", "c0003", "cnotanumber", "c0001@example.com"} {
		if err := db.Create(&model.ClientRecord{Email: email, Enable: true}).Error; err != nil {
			t.Fatalf("seed %q: %v", email, err)
		}
	}
	svc := &ClientService{}
	result, _, err := svc.GenerateCardKeys(&InboundService{}, CardKeyGenerateParams{
		InboundID:    inbound.Id,
		Count:        2,
		RemarkPrefix: "c",
	})
	if err != nil {
		t.Fatalf("GenerateCardKeys: %v", err)
	}
	got := []string{result.Keys[0].Email, result.Keys[1].Email}
	if got[0] != "c0002" || got[1] != "c0004" {
		t.Fatalf("allocated emails = %v, want [c0002 c0004]", got)
	}
}

func TestGenerateCardKeysEscapesLikeWildcardsInThePrefix(t *testing.T) {
	db := initTrafficTestDB(t)
	inbound := seedCardKeyInbound(t, db, 8443)
	// Without escaping, the "_" in the prefix is a LIKE wildcard and this
	// unrelated client would be read as a_c0001, pushing the batch to a_c0002.
	if err := db.Create(&model.ClientRecord{Email: "abc0001", Enable: true}).Error; err != nil {
		t.Fatalf("seed unrelated client: %v", err)
	}
	result, _, err := (&ClientService{}).GenerateCardKeys(&InboundService{}, CardKeyGenerateParams{
		InboundID:    inbound.Id,
		Count:        1,
		RemarkPrefix: "a_c",
	})
	if err != nil {
		t.Fatalf("GenerateCardKeys: %v", err)
	}
	if result.Keys[0].Email != "a_c0001" {
		t.Fatalf("allocated %q, want a_c0001: the prefix's underscore was treated as a LIKE wildcard", result.Keys[0].Email)
	}
}

func TestGenerateCardKeysRejectsBadInput(t *testing.T) {
	db := initTrafficTestDB(t)
	inbound := seedCardKeyInbound(t, db, 8443)
	svc := &ClientService{}

	cases := []struct {
		name   string
		params CardKeyGenerateParams
	}{
		{"missing inbound", CardKeyGenerateParams{Count: 1}},
		{"unknown inbound", CardKeyGenerateParams{InboundID: 9999, Count: 1}},
		{"zero count", CardKeyGenerateParams{InboundID: inbound.Id}},
		{"negative count", CardKeyGenerateParams{InboundID: inbound.Id, Count: -1}},
		{"over the batch cap", CardKeyGenerateParams{InboundID: inbound.Id, Count: MaxCardKeyBatch + 1}},
		{"negative quota", CardKeyGenerateParams{InboundID: inbound.Id, Count: 1, TotalGB: -1}},
		{"negative expiry", CardKeyGenerateParams{InboundID: inbound.Id, Count: 1, ExpiryDays: -1}},
		{"prefix with a slash", CardKeyGenerateParams{InboundID: inbound.Id, Count: 1, RemarkPrefix: "bad/prefix"}},
		{"prefix with a space", CardKeyGenerateParams{InboundID: inbound.Id, Count: 1, RemarkPrefix: "bad prefix"}},
	}
	for _, tc := range cases {
		result, _, err := svc.GenerateCardKeys(&InboundService{}, tc.params)
		if err == nil {
			t.Errorf("%s: GenerateCardKeys succeeded, want an error", tc.name)
		}
		if len(result.Keys) != 0 {
			t.Errorf("%s: returned %d keys after a rejected call", tc.name, len(result.Keys))
		}
	}

	var count int64
	if err := db.Model(&model.ClientRecord{}).Count(&count).Error; err != nil {
		t.Fatalf("count clients: %v", err)
	}
	if count != 0 {
		t.Fatalf("a rejected generate call created %d clients, want 0", count)
	}
}

func TestExportCardKeyRowsListsClientsWithoutKeys(t *testing.T) {
	db := initTrafficTestDB(t)
	inbound := seedCardKeyInbound(t, db, 2096)
	svc := &ClientService{}

	result, _, err := svc.GenerateCardKeys(&InboundService{}, CardKeyGenerateParams{
		InboundID:    inbound.Id,
		Count:        2,
		TotalGB:      10 << 30,
		RemarkPrefix: "card",
	})
	if err != nil {
		t.Fatalf("GenerateCardKeys: %v", err)
	}
	// A client with no card key at all, attached to the same inbound, with
	// traffic recorded.
	keyless := &model.ClientRecord{Email: "keyless@x", SubID: "sub-keyless", Enable: true, Comment: "manual", TotalGB: 1024}
	if err := db.Create(keyless).Error; err != nil {
		t.Fatalf("seed keyless client: %v", err)
	}
	if err := db.Create(&model.ClientInbound{ClientId: keyless.Id, InboundId: inbound.Id}).Error; err != nil {
		t.Fatalf("attach keyless client: %v", err)
	}
	if err := db.Create(&xray.ClientTraffic{InboundId: inbound.Id, Email: "keyless@x", Enable: true, Up: 1 << 30, Down: 512 << 20}).Error; err != nil {
		t.Fatalf("seed traffic: %v", err)
	}

	rows, err := svc.ExportCardKeyRows(inbound.Id)
	if err != nil {
		t.Fatalf("ExportCardKeyRows: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3 (two card-key clients and one without)", len(rows))
	}
	byEmail := map[string]CardKeyExportRow{}
	for _, row := range rows {
		byEmail[row.Email] = row
	}
	for _, entry := range result.Keys {
		row, ok := byEmail[entry.Email]
		if !ok {
			t.Fatalf("export is missing the generated client %q", entry.Email)
		}
		if row.CSVCardKey() != entry.CardKey {
			t.Fatalf("export card key = %q, want %q", row.CSVCardKey(), entry.CardKey)
		}
		if row.CSVPorts() != "2096" {
			t.Fatalf("export ports = %q, want 2096", row.CSVPorts())
		}
	}
	keylessRow, ok := byEmail["keyless@x"]
	if !ok {
		t.Fatal("export dropped the client that has no card key")
	}
	if keylessRow.CSVCardKey() != "未生成" {
		t.Fatalf("keyless card key = %q, want 未生成", keylessRow.CSVCardKey())
	}
	if keylessRow.Up != 1<<30 || keylessRow.Down != 512<<20 {
		t.Fatalf("keyless traffic = %d/%d, want the client_traffics row", keylessRow.Up, keylessRow.Down)
	}
	if keylessRow.Comment != "manual" {
		t.Fatalf("keyless comment = %q, want manual", keylessRow.Comment)
	}

	// Without an inbound filter every client is listed.
	all, err := svc.ExportCardKeyRows(0)
	if err != nil {
		t.Fatalf("ExportCardKeyRows(0): %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("unfiltered rows = %d, want 3", len(all))
	}
	empty, err := svc.ExportCardKeyRows(4242)
	if err != nil {
		t.Fatalf("ExportCardKeyRows(unknown inbound): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("rows for an unknown inbound = %d, want 0", len(empty))
	}
}

func TestWriteCardKeyCSVLayout(t *testing.T) {
	key := cardkey.Generate()
	rows := []CardKeyExportRow{
		{
			CardKey:    cardKeyPtr(key),
			Email:      "csv@x",
			Comment:    "batch-1",
			TotalGB:    5 << 30,
			ExpiryTime: time.Date(2030, 1, 2, 3, 4, 5, 0, time.Local).UnixMilli(),
			Up:         1 << 30,
			Down:       512 << 20,
			Enable:     true,
			Ports:      []int{443, 2096},
		},
		{Email: "nokey@x", TotalGB: 0, ExpiryTime: -2 * 24 * 3600 * 1000, Enable: false},
	}
	var buf bytes.Buffer
	if err := WriteCardKeyCSV(&buf, rows); err != nil {
		t.Fatalf("WriteCardKeyCSV: %v", err)
	}
	if !strings.HasPrefix(buf.String(), "\ufeff") {
		t.Fatal("CSV is missing the UTF-8 BOM Excel needs for the Chinese headers")
	}
	reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(buf.String(), "\ufeff")))
	records, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("re-read the CSV: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("CSV lines = %d, want a header plus two rows", len(records))
	}
	wantHeader := []string{"卡密", "邮箱", "备注", "流量GB", "到期时间", "已用上传", "已用下载", "启用", "入站端口"}
	if strings.Join(records[0], "|") != strings.Join(wantHeader, "|") {
		t.Fatalf("CSV header = %v, want %v", records[0], wantHeader)
	}
	wantFirst := []string{cardkey.Format(key), "csv@x", "batch-1", "5.00", "2030-01-02 03:04:05", "1.00", "0.50", "是", "443,2096"}
	if strings.Join(records[1], "|") != strings.Join(wantFirst, "|") {
		t.Fatalf("CSV first row = %v, want %v", records[1], wantFirst)
	}
	wantSecond := []string{"未生成", "nokey@x", "", "无限制", "首次使用后2天", "无限制", "无限制", "否", ""}
	if strings.Join(records[2], "|") != strings.Join(wantSecond, "|") {
		t.Fatalf("CSV second row = %v, want %v", records[2], wantSecond)
	}
}

func TestFormatCardKeyExpiry(t *testing.T) {
	someDay := int64(1893456000000)
	cases := map[int64]string{
		0:                 "永不过期",
		-24 * 3600 * 1000: "首次使用后1天",
		-2 * 24 * 3600e3:  "首次使用后2天",
		-3600 * 1000:      "首次使用后0.04天",
		someDay:           time.UnixMilli(someDay).Local().Format("2006-01-02 15:04:05"),
	}
	for in, want := range cases {
		if got := formatCardKeyExpiry(in); got != want {
			t.Errorf("formatCardKeyExpiry(%d) = %q, want %q", in, got, want)
		}
	}
}

// The exported key must be the one that resolves back to the client, formatted
// for a human to type.
func TestExportedCardKeyResolvesToTheClient(t *testing.T) {
	db := initTrafficTestDB(t)
	key := cardkey.Generate()
	if err := db.Create(&model.ClientRecord{Email: "resolve@x", SubID: "sub-resolve", Enable: true, CardKey: cardKeyPtr(key)}).Error; err != nil {
		t.Fatalf("seed client: %v", err)
	}
	rows, err := (&ClientService{}).ExportCardKeyRows(0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("ExportCardKeyRows = %v rows, %v; want 1, nil", len(rows), err)
	}
	subID, found, err := (&ClientService{}).GetSubIDByCardKey(rows[0].CSVCardKey())
	if err != nil || !found || subID != "sub-resolve" {
		t.Fatalf("the exported card key did not resolve: %q, %v, %v", subID, found, err)
	}
}
