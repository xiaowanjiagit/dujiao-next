package epusdt

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dujiao-next/internal/constants"
)

func TestParseConfigAndNormalizeDefaults(t *testing.T) {
	cfg, err := ParseConfig(map[string]interface{}{
		"gateway_url": " https://pay.example.com/ ",
		"auth_token":  " token ",
		"notify_url":  " https://example.com/notify ",
		"return_url":  " https://example.com/return ",
	})
	if err != nil {
		t.Fatalf("parse config failed: %v", err)
	}
	if cfg.Currency != constants.SiteCurrencyDefault {
		t.Fatalf("unexpected default currency: %s", cfg.Currency)
	}
	if cfg.GatewayURL != "https://pay.example.com" {
		t.Fatalf("unexpected normalized gateway url: %s", cfg.GatewayURL)
	}
}

func TestResolveAsset(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		token      string
		network    string
		supported  bool
	}{
		{name: "USDT alias", input: epusdtChannelTypeUSDT, token: epusdtTokenUSDT, network: epusdtNetworkTron, supported: true},
		{name: "USDT TRC20", input: epusdtChannelTypeUSDTTRC20, token: epusdtTokenUSDT, network: epusdtNetworkTron, supported: true},
		{name: "USDC TRC20", input: epusdtChannelTypeUSDCTRC20, token: epusdtTokenUSDC, network: epusdtNetworkTron, supported: true},
		{name: "TRX", input: epusdtChannelTypeTRX, token: epusdtTokenTRX, network: epusdtNetworkTron, supported: true},
		{name: "Unknown", input: "unknown", supported: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ResolveAsset(tc.input)
			if ok != tc.supported {
				t.Fatalf("supported = %v, want %v", ok, tc.supported)
			}
			if !ok {
				return
			}
			if got.Token != tc.token || got.Network != tc.network {
				t.Fatalf("asset = %+v, want token=%s network=%s", got, tc.token, tc.network)
			}
		})
	}
}

func TestToPaymentStatus(t *testing.T) {
	tests := []struct {
		name   string
		status int
		expect string
	}{
		{name: "Success", status: StatusSuccess, expect: constants.PaymentStatusSuccess},
		{name: "Expired", status: StatusExpired, expect: constants.PaymentStatusExpired},
		{name: "Waiting", status: StatusWaiting, expect: constants.PaymentStatusPending},
		{name: "Unknown", status: 999, expect: constants.PaymentStatusPending},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ToPaymentStatus(tc.status); got != tc.expect {
				t.Fatalf("unexpected payment status: got %s, want %s", got, tc.expect)
			}
		})
	}
}

func TestCreatePaymentUsesGMPayPayload(t *testing.T) {
	var gotPath string
	var gotPayload map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			t.Fatalf("decode request failed: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status_code": 200,
			"message": "success",
			"data": {
				"trade_id": "TRX-1001",
				"order_id": "ORDER-1001",
				"amount": 1,
				"actual_amount": 0.14,
				"token": "USDT",
				"expiration_time": 1800,
				"payment_url": "https://pay.example.com/checkout/TRX-1001"
			}
		}`))
	}))
	defer server.Close()

	cfg := &Config{
		GatewayURL: server.URL,
		AuthToken:  "test-token",
		Currency:   "CNY",
		NotifyURL:  "https://example.com/notify",
		ReturnURL:  "https://example.com/pay",
	}

	result, err := CreatePayment(context.Background(), cfg, CreateInput{
		OrderNo:     "ORDER-1001",
		Amount:      "1",
		Name:        "debug order",
		ChannelType: epusdtChannelTypeUSDTTRC20,
	})
	if err != nil {
		t.Fatalf("CreatePayment failed: %v", err)
	}
	if gotPath != epusdtCreateTransactionPath {
		t.Fatalf("path = %s, want %s", gotPath, epusdtCreateTransactionPath)
	}
	if gotPayload["currency"] != "CNY" {
		t.Fatalf("currency = %v", gotPayload["currency"])
	}
	if gotPayload["token"] != epusdtTokenUSDT {
		t.Fatalf("token = %v", gotPayload["token"])
	}
	if gotPayload["network"] != epusdtNetworkTron {
		t.Fatalf("network = %v", gotPayload["network"])
	}
	if _, exists := gotPayload["trade_type"]; exists {
		t.Fatalf("trade_type should not be sent in gmpay payload")
	}
	if _, exists := gotPayload["fiat"]; exists {
		t.Fatalf("fiat should not be sent in gmpay payload")
	}
	if result.Amount != "1" {
		t.Fatalf("amount = %s, want 1", result.Amount)
	}
	if result.ActualAmount != "0.14" {
		t.Fatalf("actual_amount = %s, want 0.14", result.ActualAmount)
	}
}

func TestCreatePaymentAcceptsMsgField(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status_code": 200,
			"msg": "success",
			"data": {
				"trade_id": "TRX-1002",
				"order_id": "ORDER-1002",
				"amount": "1.00",
				"actual_amount": "0.15",
				"token": "USDT",
				"expiration_time": 1800,
				"payment_url": "https://pay.example.com/checkout/TRX-1002"
			}
		}`))
	}))
	defer server.Close()

	cfg := &Config{
		GatewayURL: server.URL,
		AuthToken:  "test-token",
		Currency:   "CNY",
		NotifyURL:  "https://example.com/notify",
		ReturnURL:  "https://example.com/pay",
	}

	result, err := CreatePayment(context.Background(), cfg, CreateInput{
		OrderNo:     "ORDER-1002",
		Amount:      "1",
		ChannelType: epusdtChannelTypeUSDTTRC20,
	})
	if err != nil {
		t.Fatalf("CreatePayment failed: %v", err)
	}
	if result.TradeID != "TRX-1002" {
		t.Fatalf("trade_id = %s", result.TradeID)
	}
}
