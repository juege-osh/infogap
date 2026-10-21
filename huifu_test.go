package main

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testHuifuClient(t *testing.T) *huifuPayClient {
	t.Helper()
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	return &huifuPayClient{baseURL: "https://gateway.example", sysID: "test-system", productID: "test-product", huifuID: "test-merchant", privateKey: key, publicKey: &key.PublicKey, notifyURL: "https://infogap.example/api/billing/notify/huifu", connectTimeout: time.Second, readTimeout: time.Second}
}
func huifuSuccessData() map[string]any {
	return map[string]any{"resp_code": "00000000", "huifu_id": "test-merchant", "org_req_seq_id": "P20261021IG12345678901234567890", "org_hf_seq_id": "platform-transaction", "trans_amt": "9.90", "trans_stat": "S", "order_stat": "1"}
}
func TestHuifuNotificationRequiresAuthenticCompletePayment(t *testing.T) {
	c := testHuifuClient(t)
	variants := map[string]func(map[string]any){"valid": func(map[string]any) {}, "wrong_merchant": func(m map[string]any) { m["huifu_id"] = "other" }, "missing_order": func(m map[string]any) { delete(m, "org_req_seq_id") }, "missing_transaction": func(m map[string]any) { delete(m, "org_hf_seq_id") }, "missing_success_state": func(m map[string]any) { delete(m, "order_stat") }, "invalid_success_state": func(m map[string]any) { m["order_stat"] = "2" }, "nonfinal_status": func(m map[string]any) { m["trans_stat"] = "P" }}
	for name, mutate := range variants {
		t.Run(name, func(t *testing.T) {
			data := huifuSuccessData()
			mutate(data)
			raw, _ := json.Marshal(data)
			sig, _ := signHuifuRaw(string(raw), c.privateKey)
			_, e := c.VerifyNotification(string(raw), sig)
			if (name == "valid") != (e == nil) {
				t.Fatalf("unexpected verification %v", e)
			}
		})
	}
	data := huifuSuccessData()
	raw, _ := json.Marshal(data)
	sig, _ := signHuifuRaw(string(raw), c.privateKey)
	tampered := strings.Replace(string(raw), "9.90", "0.01", 1)
	if _, e := c.VerifyNotification(tampered, sig); e == nil {
		t.Fatal("tampered signed amount accepted")
	}
	if _, e := c.VerifyNotification(string(raw), "invalid"); e == nil {
		t.Fatal("invalid signature accepted")
	}
}
func TestHuifuQueryChecksSignedMerchantOrderAmountAndState(t *testing.T) {
	c := testHuifuClient(t)
	variants := map[string]func(map[string]any){"valid": func(map[string]any) {}, "merchant": func(m map[string]any) { m["huifu_id"] = "other" }, "order": func(m map[string]any) { m["org_req_seq_id"] = "other" }, "missing_order": func(m map[string]any) { delete(m, "org_req_seq_id") }, "amount": func(m map[string]any) { m["trans_amt"] = "0.01" }, "zero_amount": func(m map[string]any) { m["trans_amt"] = "0.00" }, "state": func(m map[string]any) { m["order_stat"] = "2" }, "missing_state": func(m map[string]any) { delete(m, "order_stat") }, "missing_transaction": func(m map[string]any) { delete(m, "org_hf_seq_id") }}
	for name, mutate := range variants {
		t.Run(name, func(t *testing.T) {
			data := huifuSuccessData()
			mutate(data)
			ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Data map[string]any `json:"data"`
					Sign string         `json:"sign"`
				}
				if e := json.NewDecoder(r.Body).Decode(&request); e != nil {
					t.Error(e)
				}
				if !verifyHuifuData(request.Data, request.Sign, c.publicKey) {
					t.Error("outgoing request not signed")
				}
				if request.Data["org_req_seq_id"] != "P20261021IG12345678901234567890" || request.Data["org_req_date"] != "20261021" {
					t.Error("query request identity wrong")
				}
				sig, _ := signHuifuData(data, c.privateKey)
				json.NewEncoder(w).Encode(map[string]any{"data": data, "sign": sig})
			}))
			defer ts.Close()
			c.baseURL, c.httpClient = ts.URL, ts.Client()
			_, e := c.QueryPayment("P20261021IG12345678901234567890", 990)
			if (name == "valid") != (e == nil) {
				t.Fatalf("unexpected query validation %v", e)
			}
		})
	}
}
func TestHuifuPreorderUsesExactAmountCallbackAndSignedReply(t *testing.T) {
	c := testHuifuClient(t)
	id := "P20261021IG12345678901234567890"
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Data map[string]any `json:"data"`
			Sign string         `json:"sign"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if !verifyHuifuData(req.Data, req.Sign, c.publicKey) || req.Data["trans_amt"] != "9.90" || req.Data["notify_url"] != c.notifyURL || req.Data["req_seq_id"] != id || req.Data["trade_type"] != "T_NATIVE" {
			t.Error("preorder payload mismatch")
		}
		data := map[string]any{"resp_code": "00000100", "huifu_id": c.huifuID, "req_seq_id": id, "qr_code": "weixin://wxpay/bizpayurl?pr=test"}
		sig, _ := signHuifuData(data, c.privateKey)
		json.NewEncoder(w).Encode(map[string]any{"data": data, "sign": sig})
	}))
	defer ts.Close()
	c.baseURL, c.httpClient = ts.URL, ts.Client()
	result, e := c.CreatePayment(id, "InfoGap 30天订阅会员", 990, "wechat", time.Now().Add(time.Minute))
	if e != nil || result.PaymentNo != id || result.MerchantID != c.huifuID {
		t.Fatal(result, e)
	}
}
func TestHuifuUnsignedGatewayResponseRejected(t *testing.T) {
	c := testHuifuClient(t)
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"data": huifuSuccessData(), "sign": "wrong"})
	}))
	defer ts.Close()
	c.baseURL, c.httpClient = ts.URL, ts.Client()
	if _, e := c.QueryPayment("P20261021IG12345678901234567890", 990); e == nil {
		t.Fatal("unsigned gateway accepted")
	}
}
