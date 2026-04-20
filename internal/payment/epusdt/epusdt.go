package epusdt

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/payment/common"
)

var (
	ErrConfigInvalid        = errors.New("epusdt config invalid")
	ErrRequestFailed        = errors.New("epusdt request failed")
	ErrResponseInvalid      = errors.New("epusdt response invalid")
	ErrSignatureInvalid     = errors.New("epusdt signature invalid")
	ErrChannelTypeNotSupport = errors.New("epusdt channel type not supported")
)

const (
	StatusWaiting = 1
	StatusSuccess = 2
	StatusExpired = 3

	epusdtChannelTypeUSDT      = "usdt"
	epusdtChannelTypeUSDTTRC20 = "usdt-trc20"
	epusdtChannelTypeUSDCTRC20 = "usdc-trc20"
	epusdtChannelTypeTRX       = "trx"

	epusdtTokenUSDT = "usdt"
	epusdtTokenUSDC = "usdc"
	epusdtTokenTRX  = "trx"

	epusdtNetworkTron = "tron"

	epusdtCreateTransactionPath = "/payments/gmpay/v1/order/create-transaction"
	epusdtStatusSuccessMsg      = "status is not success"
)

type Config struct {
	GatewayURL string `json:"gateway_url"`
	AuthToken  string `json:"auth_token"`
	Currency   string `json:"currency"`
	NotifyURL  string `json:"notify_url"`
	ReturnURL  string `json:"return_url"`
}

type CreateInput struct {
	OrderNo     string
	Amount      string
	Name        string
	NotifyURL   string
	ReturnURL   string
	ChannelType string
}

type CreateResult struct {
	TradeID      string
	OrderID      string
	Amount       string
	ActualAmount string
	Token        string
	PaymentURL   string
	Raw          map[string]interface{}
}

type CallbackData struct {
	TradeID            string      `json:"trade_id"`
	OrderID            string      `json:"order_id"`
	Amount             interface{} `json:"amount"`
	ActualAmount       interface{} `json:"actual_amount"`
	Token              string      `json:"token"`
	BlockTransactionID string      `json:"block_transaction_id"`
	Signature          string      `json:"signature"`
	Status             int         `json:"status"`
}

type asset struct {
	Token   string
	Network string
}

func (c *CallbackData) GetAmount() float64 {
	switch v := c.Amount.(type) {
	case float64:
		return v
	case string:
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return 0
}

func (c *CallbackData) GetActualAmount() float64 {
	switch v := c.ActualAmount.(type) {
	case float64:
		return v
	case string:
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return 0
}

func ParseConfig(raw map[string]interface{}) (*Config, error) {
	return common.ParseConfig[Config](raw, ErrConfigInvalid)
}

func ValidateConfig(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("%w: config is nil", ErrConfigInvalid)
	}
	if strings.TrimSpace(cfg.GatewayURL) == "" {
		return fmt.Errorf("%w: gateway_url is required", ErrConfigInvalid)
	}
	if strings.TrimSpace(cfg.AuthToken) == "" {
		return fmt.Errorf("%w: auth_token is required", ErrConfigInvalid)
	}
	if strings.TrimSpace(cfg.NotifyURL) == "" {
		return fmt.Errorf("%w: notify_url is required", ErrConfigInvalid)
	}
	if strings.TrimSpace(cfg.ReturnURL) == "" {
		return fmt.Errorf("%w: return_url is required", ErrConfigInvalid)
	}
	return nil
}

func (c *Config) Normalize() {
	c.GatewayURL = strings.TrimRight(strings.TrimSpace(c.GatewayURL), "/")
	c.AuthToken = strings.TrimSpace(c.AuthToken)
	c.Currency = strings.ToUpper(strings.TrimSpace(c.Currency))
	c.NotifyURL = strings.TrimSpace(c.NotifyURL)
	c.ReturnURL = strings.TrimSpace(c.ReturnURL)
	if c.Currency == "" {
		c.Currency = constants.SiteCurrencyDefault
	}
}

func CreatePayment(ctx context.Context, cfg *Config, input CreateInput) (*CreateResult, error) {
	if cfg == nil {
		return nil, ErrConfigInvalid
	}
	if input.OrderNo == "" || input.Amount == "" || input.ChannelType == "" {
		return nil, ErrConfigInvalid
	}

	targetAsset, ok := ResolveAsset(input.ChannelType)
	if !ok {
		return nil, ErrChannelTypeNotSupport
	}

	notifyURL := input.NotifyURL
	if notifyURL == "" {
		notifyURL = cfg.NotifyURL
	}
	returnURL := input.ReturnURL
	if returnURL == "" {
		returnURL = cfg.ReturnURL
	}

	amountFloat, err := strconv.ParseFloat(input.Amount, 64)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid amount", ErrConfigInvalid)
	}

	params := map[string]interface{}{
		"order_id":     input.OrderNo,
		"amount":       amountFloat,
		"currency":     cfg.Currency,
		"token":        targetAsset.Token,
		"network":      targetAsset.Network,
		"notify_url":   notifyURL,
		"redirect_url": returnURL,
	}
	if input.Name != "" {
		params["name"] = input.Name
	}

	params["signature"] = Sign(params, cfg.AuthToken)

	endpoint := cfg.GatewayURL + epusdtCreateTransactionPath
	respBytes, err := postJSON(ctx, endpoint, params)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRequestFailed, err)
	}

	var resp struct {
		StatusCode int    `json:"status_code"`
		Message    string `json:"message"`
		Msg        string `json:"msg"`
		Data       struct {
			Currency       string      `json:"currency"`
			TradeID        string      `json:"trade_id"`
			OrderID        string      `json:"order_id"`
			Amount         interface{} `json:"amount"`
			ActualAmount   interface{} `json:"actual_amount"`
			Token          string      `json:"token"`
			ExpirationTime int         `json:"expiration_time"`
			PaymentURL     string      `json:"payment_url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrResponseInvalid, err)
	}
	message := strings.TrimSpace(resp.Message)
	if message == "" {
		message = strings.TrimSpace(resp.Msg)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%w: %s", ErrResponseInvalid, message)
	}

	amount, err := stringifyJSONNumber(resp.Data.Amount)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid amount field", ErrResponseInvalid)
	}
	actualAmount, err := stringifyJSONNumber(resp.Data.ActualAmount)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid actual_amount field", ErrResponseInvalid)
	}

	var raw map[string]interface{}
	_ = json.Unmarshal(respBytes, &raw)

	return &CreateResult{
		TradeID:      resp.Data.TradeID,
		OrderID:      resp.Data.OrderID,
		Amount:       amount,
		ActualAmount: actualAmount,
		Token:        resp.Data.Token,
		PaymentURL:   resp.Data.PaymentURL,
		Raw:          raw,
	}, nil
}

func VerifyCallback(cfg *Config, data *CallbackData) error {
	if cfg == nil || data == nil {
		return ErrConfigInvalid
	}

	if data.Status != StatusSuccess {
		return fmt.Errorf("%w: %s", ErrResponseInvalid, epusdtStatusSuccessMsg)
	}

	params := map[string]interface{}{
		"trade_id":             data.TradeID,
		"order_id":             data.OrderID,
		"amount":               data.GetAmount(),
		"actual_amount":        data.GetActualAmount(),
		"token":                data.Token,
		"block_transaction_id": data.BlockTransactionID,
		"status":               data.Status,
	}

	expected := Sign(params, cfg.AuthToken)
	if !strings.EqualFold(expected, data.Signature) {
		return ErrSignatureInvalid
	}
	return nil
}

func ParseCallback(body []byte) (*CallbackData, error) {
	if len(body) == 0 {
		return nil, ErrResponseInvalid
	}
	var data CallbackData
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrResponseInvalid, err)
	}
	return &data, nil
}

func Sign(params map[string]interface{}, authToken string) string {
	var keys []string
	for k, v := range params {
		if k == "signature" {
			continue
		}
		if isEmptyValue(v) {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var pairs []string
	for _, k := range keys {
		v := params[k]
		pairs = append(pairs, fmt.Sprintf("%s=%v", k, v))
	}

	content := strings.Join(pairs, "&") + authToken
	sum := md5.Sum([]byte(content))
	return strings.ToLower(hex.EncodeToString(sum[:]))
}

func isEmptyValue(v interface{}) bool {
	if v == nil {
		return true
	}
	switch val := v.(type) {
	case string:
		return strings.TrimSpace(val) == ""
	case int, int8, int16, int32, int64:
		return false
	case uint, uint8, uint16, uint32, uint64:
		return false
	case float32, float64:
		return false
	case bool:
		return false
	default:
		return false
	}
}

func stringifyJSONNumber(v interface{}) (string, error) {
	switch val := v.(type) {
	case nil:
		return "", nil
	case string:
		return strings.TrimSpace(val), nil
	case float64:
		return strconv.FormatFloat(val, 'f', -1, 64), nil
	case float32:
		return strconv.FormatFloat(float64(val), 'f', -1, 64), nil
	case int:
		return strconv.Itoa(val), nil
	case int8, int16, int32, int64:
		return fmt.Sprintf("%d", val), nil
	case uint, uint8, uint16, uint32, uint64:
		return fmt.Sprintf("%d", val), nil
	case json.Number:
		return val.String(), nil
	default:
		return "", fmt.Errorf("unsupported number type %T", v)
	}
}

func postJSON(ctx context.Context, endpoint string, params map[string]interface{}) ([]byte, error) {
	body, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("http status %d", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}

func IsSupportedChannelType(channelType string) bool {
	_, ok := ResolveAsset(channelType)
	return ok
}

func ResolveAsset(channelType string) (asset, bool) {
	switch strings.ToLower(strings.TrimSpace(channelType)) {
	case epusdtChannelTypeUSDT, epusdtChannelTypeUSDTTRC20:
		return asset{Token: epusdtTokenUSDT, Network: epusdtNetworkTron}, true
	case epusdtChannelTypeUSDCTRC20:
		return asset{Token: epusdtTokenUSDC, Network: epusdtNetworkTron}, true
	case epusdtChannelTypeTRX:
		return asset{Token: epusdtTokenTRX, Network: epusdtNetworkTron}, true
	default:
		return asset{}, false
	}
}

func ToPaymentStatus(status int) string {
	switch status {
	case StatusSuccess:
		return constants.PaymentStatusSuccess
	case StatusExpired:
		return constants.PaymentStatusExpired
	default:
		return constants.PaymentStatusPending
	}
}
