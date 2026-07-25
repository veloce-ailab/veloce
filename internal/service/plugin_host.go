package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/shopspring/decimal"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"gorm.io/gorm/clause"
)

const (
	pluginHostModule     = "veloce"
	pluginHostCallExport = "host_call"
	pluginHostMaxRequest = 1 << 20
)

const pluginChannelHTTPMaxResponse = 1 << 20

var pluginChannelHTTPClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		Proxy:       nil,
		DialContext: pluginChannelHTTPDialContext,
	},
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
}

const (
	pluginHostOK uint32 = iota
	pluginHostBufferTooSmall
	pluginHostInvalidRequest
	pluginHostDenied
	pluginHostNotFound
	pluginHostConflict
	pluginHostInsufficientBalance
	pluginHostInternal
)

type pluginRuntimeInvocation struct {
	UserID    uint
	RequestID string
}

func instantiatePluginHost(ctx context.Context, runtime wazero.Runtime, plugin model.Plugin, invocation pluginRuntimeInvocation) error {
	_, err := runtime.NewHostModuleBuilder(pluginHostModule).
		NewFunctionBuilder().
		WithFunc(func(callCtx context.Context, module api.Module, opPtr, opLen, requestPtr, requestLen, responsePtr, responseCap uint32) uint64 {
			return pluginHostCall(callCtx, module, plugin, invocation, opPtr, opLen, requestPtr, requestLen, responsePtr, responseCap)
		}).
		Export(pluginHostCallExport).
		Instantiate(ctx)
	return err
}

func pluginHostCall(ctx context.Context, module api.Module, plugin model.Plugin, invocation pluginRuntimeInvocation, opPtr, opLen, requestPtr, requestLen, responsePtr, responseCap uint32) uint64 {
	if opLen > pluginHostMaxRequest || requestLen > pluginHostMaxRequest {
		return pluginHostWriteResponse(module, responsePtr, responseCap, pluginHostInvalidRequest, pluginHostError("request_too_large", "plugin host request is too large"))
	}
	memory := module.Memory()
	if memory == nil {
		return pluginHostPack(pluginHostInternal, 0)
	}
	opRaw, ok := memory.Read(opPtr, opLen)
	if !ok {
		return pluginHostPack(pluginHostInvalidRequest, 0)
	}
	requestRaw, ok := memory.Read(requestPtr, requestLen)
	if !ok {
		return pluginHostPack(pluginHostInvalidRequest, 0)
	}
	var request map[string]interface{}
	if len(requestRaw) > 0 {
		if err := json.Unmarshal(requestRaw, &request); err != nil {
			return pluginHostWriteResponse(module, responsePtr, responseCap, pluginHostInvalidRequest, pluginHostError("invalid_json", "plugin host request must be a JSON object"))
		}
	}
	result, status := executePluginHostCall(ctx, plugin, invocation, string(opRaw), request)
	return pluginHostWriteResponse(module, responsePtr, responseCap, status, result)
}

func pluginHostWriteResponse(module api.Module, responsePtr, responseCap uint32, status uint32, payload map[string]interface{}) uint64 {
	raw, err := json.Marshal(payload)
	if err != nil {
		raw = []byte(`{"ok":false,"code":"internal_error","error":"failed to encode host response"}`)
		status = pluginHostInternal
	}
	if uint64(len(raw)) > uint64(responseCap) {
		return pluginHostPack(pluginHostBufferTooSmall, uint32(len(raw)))
	}
	if len(raw) > 0 && !module.Memory().Write(responsePtr, raw) {
		return pluginHostPack(pluginHostInvalidRequest, 0)
	}
	return pluginHostPack(status, uint32(len(raw)))
}

func pluginHostPack(status, value uint32) uint64 {
	return uint64(status)<<32 | uint64(value)
}

func executePluginHostCall(ctx context.Context, plugin model.Plugin, invocation pluginRuntimeInvocation, operation string, request map[string]interface{}) (map[string]interface{}, uint32) {
	operation = strings.TrimSpace(operation)
	switch operation {
	case "wallet.balance":
		if !pluginHasPermission(plugin, "wallet.balance.read") {
			return pluginHostError("permission_denied", "plugin lacks wallet.balance.read permission"), pluginHostDenied
		}
		if model.DB == nil || invocation.UserID == 0 {
			return pluginHostError("invalid_context", "wallet operations require an authenticated user"), pluginHostInvalidRequest
		}
		var user model.User
		if err := model.DB.WithContext(ctx).Select("id", "balance").First(&user, invocation.UserID).Error; err != nil {
			return pluginHostError("user_not_found", "user was not found"), pluginHostNotFound
		}
		return map[string]interface{}{"ok": true, "balance": user.Balance.String()}, pluginHostOK
	case "wallet.settle":
		return executePluginWalletSettlement(ctx, plugin, invocation, request)
	case "wallet.transaction":
		return executePluginWalletTransaction(ctx, plugin, invocation, request)
	case "plugin.kv.get":
		return executePluginKVGet(ctx, plugin, invocation, request)
	case "plugin.kv.put":
		return executePluginKVPut(ctx, plugin, invocation, request)
	case "plugin.kv.delete":
		return executePluginKVDelete(ctx, plugin, invocation, request)
	case "plugin.log":
		return executePluginLog(invocation, plugin, request)
	case "plugin.channel.http":
		return executePluginChannelHTTP(ctx, plugin, request)
	default:
		return pluginHostError("unknown_operation", "unknown plugin host operation: "+operation), pluginHostInvalidRequest
	}
}

func executePluginChannelHTTP(ctx context.Context, plugin model.Plugin, request map[string]interface{}) (map[string]interface{}, uint32) {
	if !pluginHasPermission(plugin, "plugin.channel.http") {
		return pluginHostError("permission_denied", "plugin lacks plugin.channel.http permission"), pluginHostDenied
	}
	method := strings.ToUpper(stringRequest(request, "method"))
	if method == "" {
		method = http.MethodPost
	}
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return pluginHostError("invalid_request", "HTTP method is not allowed"), pluginHostInvalidRequest
	}
	rawURL := stringRequest(request, "url")
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Host == "" {
		return pluginHostError("invalid_request", "HTTP url must be an absolute http or https URL"), pluginHostInvalidRequest
	}
	if parsed.User != nil || parsed.Hostname() == "" {
		return pluginHostError("invalid_request", "HTTP url must not include user credentials"), pluginHostInvalidRequest
	}
	body := stringRequest(request, "body")
	if len(body) > pluginHostMaxRequest {
		return pluginHostError("invalid_request", "HTTP request body is too large"), pluginHostInvalidRequest
	}
	httpRequest, err := http.NewRequestWithContext(ctx, method, parsed.String(), strings.NewReader(body))
	if err != nil {
		return pluginHostError("invalid_request", err.Error()), pluginHostInvalidRequest
	}
	if headers, ok := request["headers"].(map[string]interface{}); ok {
		if len(headers) > 50 {
			return pluginHostError("invalid_request", "too many HTTP headers"), pluginHostInvalidRequest
		}
		for key, value := range headers {
			name := strings.TrimSpace(key)
			text, ok := value.(string)
			if name == "" || !ok || len(name) > 120 || len(text) > 4096 || strings.EqualFold(name, "Host") {
				return pluginHostError("invalid_request", "invalid HTTP header"), pluginHostInvalidRequest
			}
			httpRequest.Header.Set(name, text)
		}
	}
	response, err := pluginChannelHTTPClient.Do(httpRequest)
	if err != nil {
		return pluginHostError("http_error", err.Error()), pluginHostInternal
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, pluginChannelHTTPMaxResponse+1))
	if err != nil {
		return pluginHostError("http_error", err.Error()), pluginHostInternal
	}
	if len(responseBody) > pluginChannelHTTPMaxResponse {
		return pluginHostError("response_too_large", "HTTP response body exceeds 1 MiB"), pluginHostInternal
	}
	responseHeaders := map[string]string{}
	for key, values := range response.Header {
		if len(responseHeaders) >= 50 {
			break
		}
		responseHeaders[key] = strings.Join(values, ", ")
	}
	return map[string]interface{}{"ok": true, "status": response.StatusCode, "headers": responseHeaders, "body": string(responseBody)}, pluginHostOK
}

// pluginChannelHTTPDialContext resolves first and dials only public IPs. This
// prevents a plugin from reaching the host, container, or private network.
func pluginChannelHTTPDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{}
	var lastErr error
	for _, candidate := range ips {
		if !pluginChannelHTTPPublicIP(candidate.IP) {
			lastErr = errors.New("HTTP target resolves to a non-public address")
			continue
		}
		conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(candidate.IP.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	if lastErr == nil {
		lastErr = errors.New("HTTP target has no public addresses")
	}
	return nil, lastErr
}

func pluginChannelHTTPPublicIP(ip net.IP) bool {
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsUnspecified() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return false
	}
	if ipv4 := ip.To4(); ipv4 != nil {
		// CGNAT and benchmark ranges are not private according to net.IP, but
		// must not be reachable from a plugin-managed HTTP client either.
		if ipv4[0] == 0 || ipv4[0] == 100 && ipv4[1] >= 64 && ipv4[1] <= 127 || ipv4[0] == 198 && ipv4[1]&0xfe == 18 {
			return false
		}
	}
	return true
}

func executePluginWalletSettlement(ctx context.Context, plugin model.Plugin, invocation pluginRuntimeInvocation, request map[string]interface{}) (map[string]interface{}, uint32) {
	debit, err := decimalFromRequest(request, "debit")
	if err != nil {
		return pluginHostError("invalid_amount", err.Error()), pluginHostInvalidRequest
	}
	credit, err := decimalFromRequest(request, "credit")
	if err != nil {
		return pluginHostError("invalid_amount", err.Error()), pluginHostInvalidRequest
	}
	if debit.GreaterThan(decimal.Zero) && !pluginHasPermission(plugin, "wallet.balance.debit") {
		return pluginHostError("permission_denied", "plugin lacks wallet.balance.debit permission"), pluginHostDenied
	}
	if credit.GreaterThan(decimal.Zero) && !pluginHasPermission(plugin, "wallet.balance.credit") {
		return pluginHostError("permission_denied", "plugin lacks wallet.balance.credit permission"), pluginHostDenied
	}
	idempotencyKey := stringRequest(request, "idempotency_key")
	if idempotencyKey == "" {
		idempotencyKey = invocation.RequestID
	}
	metadata, _ := request["metadata"].(map[string]interface{})
	limits, err := pluginWalletLimits(metadata)
	if err != nil {
		return pluginHostError("invalid_limit", err.Error()), pluginHostInvalidRequest
	}
	result, err := SettleWallet(ctx, WalletSettlementInput{
		UserID: invocation.UserID, Source: "plugin:" + plugin.ID, PluginID: plugin.ID,
		IdempotencyKey: idempotencyKey, DebitAmount: debit, CreditAmount: credit,
		ReferenceType: stringRequest(request, "reference_type"), ReferenceID: stringRequest(request, "reference_id"),
		Description: stringRequest(request, "description"), Metadata: metadata, Limits: limits,
	})
	if err != nil {
		return pluginHostError(walletErrorCode(err), err.Error()), pluginHostStatus(err)
	}
	return pluginWalletTransactionResponse(result.Transaction, result.Replay), pluginHostOK
}

func executePluginWalletTransaction(ctx context.Context, plugin model.Plugin, invocation pluginRuntimeInvocation, request map[string]interface{}) (map[string]interface{}, uint32) {
	if !pluginHasPermission(plugin, "wallet.balance.read") {
		return pluginHostError("permission_denied", "plugin lacks wallet.balance.read permission"), pluginHostDenied
	}
	idempotencyKey := stringRequest(request, "idempotency_key")
	if idempotencyKey == "" {
		idempotencyKey = invocation.RequestID
	}
	if model.DB == nil || invocation.UserID == 0 || idempotencyKey == "" {
		return pluginHostError("invalid_request", "a user and idempotency key are required"), pluginHostInvalidRequest
	}
	entry, found, err := findWalletTransaction(model.DB.WithContext(ctx), WalletSettlementInput{UserID: invocation.UserID, Source: "plugin:" + plugin.ID, IdempotencyKey: idempotencyKey})
	if err != nil {
		return pluginHostError("wallet_error", err.Error()), pluginHostInternal
	}
	if !found {
		return map[string]interface{}{"ok": true, "found": false}, pluginHostOK
	}
	response := pluginWalletTransactionResponse(entry, true)
	response["found"] = true
	return response, pluginHostOK
}

func pluginWalletTransactionResponse(entry model.WalletTransaction, replay bool) map[string]interface{} {
	var settledMetadata map[string]interface{}
	_ = json.Unmarshal([]byte(entry.MetadataJSON), &settledMetadata)
	return map[string]interface{}{
		"ok": true, "replay": replay, "transaction_id": entry.ID,
		"debit": entry.DebitAmount.String(), "credit": entry.CreditAmount.String(),
		"balance_before": entry.BalanceBefore.String(), "balance_after": entry.BalanceAfter.String(),
		"metadata": settledMetadata,
	}
}

func executePluginKVGet(ctx context.Context, plugin model.Plugin, invocation pluginRuntimeInvocation, request map[string]interface{}) (map[string]interface{}, uint32) {
	if !pluginHasPermission(plugin, "plugin.kv.read") {
		return pluginHostError("permission_denied", "plugin lacks plugin.kv.read permission"), pluginHostDenied
	}
	key, ok := pluginKVKey(request)
	if !ok || model.DB == nil || invocation.UserID == 0 {
		return pluginHostError("invalid_request", "a user and non-empty KV key are required"), pluginHostInvalidRequest
	}
	var value model.PluginKV
	err := model.DB.WithContext(ctx).Where("user_id = ? AND plugin_id = ? AND key = ?", invocation.UserID, plugin.ID, key).Limit(1).Find(&value).Error
	if err != nil {
		return pluginHostError("storage_error", err.Error()), pluginHostInternal
	}
	if value.ID == 0 {
		return map[string]interface{}{"ok": true, "found": false}, pluginHostOK
	}
	var decoded interface{}
	if err := json.Unmarshal([]byte(value.ValueJSON), &decoded); err != nil {
		return pluginHostError("storage_error", "stored plugin KV value is invalid JSON"), pluginHostInternal
	}
	return map[string]interface{}{"ok": true, "found": true, "value": decoded}, pluginHostOK
}

func executePluginKVPut(ctx context.Context, plugin model.Plugin, invocation pluginRuntimeInvocation, request map[string]interface{}) (map[string]interface{}, uint32) {
	if !pluginHasPermission(plugin, "plugin.kv.write") {
		return pluginHostError("permission_denied", "plugin lacks plugin.kv.write permission"), pluginHostDenied
	}
	key, ok := pluginKVKey(request)
	value, hasValue := request["value"]
	if !ok || !hasValue || model.DB == nil || invocation.UserID == 0 {
		return pluginHostError("invalid_request", "a user, KV key, and value are required"), pluginHostInvalidRequest
	}
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > pluginHostMaxRequest {
		return pluginHostError("invalid_request", "KV value must be valid JSON and smaller than 1 MiB"), pluginHostInvalidRequest
	}
	entry := model.PluginKV{UserID: invocation.UserID, PluginID: plugin.ID, Key: key, ValueJSON: string(raw)}
	err = model.DB.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "user_id"}, {Name: "plugin_id"}, {Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"value_json", "updated_at"})}).Create(&entry).Error
	if err != nil {
		return pluginHostError("storage_error", err.Error()), pluginHostInternal
	}
	return map[string]interface{}{"ok": true}, pluginHostOK
}

func executePluginKVDelete(ctx context.Context, plugin model.Plugin, invocation pluginRuntimeInvocation, request map[string]interface{}) (map[string]interface{}, uint32) {
	if !pluginHasPermission(plugin, "plugin.kv.write") {
		return pluginHostError("permission_denied", "plugin lacks plugin.kv.write permission"), pluginHostDenied
	}
	key, ok := pluginKVKey(request)
	if !ok || model.DB == nil || invocation.UserID == 0 {
		return pluginHostError("invalid_request", "a user and non-empty KV key are required"), pluginHostInvalidRequest
	}
	if err := model.DB.WithContext(ctx).Where("user_id = ? AND plugin_id = ? AND key = ?", invocation.UserID, plugin.ID, key).Delete(&model.PluginKV{}).Error; err != nil {
		return pluginHostError("storage_error", err.Error()), pluginHostInternal
	}
	return map[string]interface{}{"ok": true}, pluginHostOK
}

func executePluginLog(invocation pluginRuntimeInvocation, plugin model.Plugin, request map[string]interface{}) (map[string]interface{}, uint32) {
	level := stringRequest(request, "level")
	if level == "" {
		level = "info"
	}
	if level != "info" && level != "warn" && level != "error" {
		return pluginHostError("invalid_request", "log level must be info, warn, or error"), pluginHostInvalidRequest
	}
	message := stringRequest(request, "message")
	if message == "" || len(message) > 2000 {
		return pluginHostError("invalid_request", "log message is required and must be at most 2000 bytes"), pluginHostInvalidRequest
	}
	metadata, _ := json.Marshal(request["metadata"])
	recordPluginLog(invocation.UserID, plugin.ID, level, "plugin_log", message, string(metadata))
	return map[string]interface{}{"ok": true}, pluginHostOK
}

func pluginHasPermission(plugin model.Plugin, permission string) bool {
	for _, candidate := range decodePluginStringList(plugin.PermissionsJSON) {
		if candidate == permission || candidate == "*" {
			return true
		}
	}
	return false
}

func pluginKVKey(request map[string]interface{}) (string, bool) {
	key := stringRequest(request, "key")
	return key, key != "" && len(key) <= 200
}

func stringRequest(request map[string]interface{}, key string) string {
	value, _ := request[key].(string)
	return strings.TrimSpace(value)
}

func decimalFromRequest(request map[string]interface{}, key string) (decimal.Decimal, error) {
	raw, exists := request[key]
	if !exists || raw == nil {
		return decimal.Zero, nil
	}
	value, ok := raw.(string)
	if !ok || strings.TrimSpace(value) == "" {
		return decimal.Zero, errors.New(key + " must be a decimal string")
	}
	amount, err := decimal.NewFromString(strings.TrimSpace(value))
	if err != nil {
		return decimal.Zero, errors.New(key + " must be a valid decimal string")
	}
	return amount, nil
}

func pluginHostError(code, message string) map[string]interface{} {
	return map[string]interface{}{"ok": false, "code": code, "error": message}
}

func walletErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrInsufficientBalance):
		return "insufficient_balance"
	case errors.Is(err, ErrWalletIdempotencyConflict):
		return "idempotency_conflict"
	case errors.Is(err, ErrWalletInvalidSettlement):
		return "invalid_settlement"
	case errors.Is(err, ErrWalletLimitExceeded):
		return "participation_limit"
	default:
		return "wallet_error"
	}
}

func pluginHostStatus(err error) uint32 {
	switch {
	case errors.Is(err, ErrInsufficientBalance):
		return pluginHostInsufficientBalance
	case errors.Is(err, ErrWalletIdempotencyConflict):
		return pluginHostConflict
	case errors.Is(err, ErrWalletInvalidSettlement):
		return pluginHostInvalidRequest
	case errors.Is(err, ErrWalletLimitExceeded):
		return pluginHostConflict
	default:
		return pluginHostInternal
	}
}

func pluginWalletLimits(metadata map[string]interface{}) ([]WalletSettlementLimit, error) {
	if metadata == nil {
		return nil, nil
	}
	raw, exists := metadata["_limits"]
	if !exists {
		return nil, nil
	}
	items, ok := raw.([]interface{})
	if !ok {
		return nil, errors.New("metadata._limits must be an array")
	}
	limits := make([]WalletSettlementLimit, 0, len(items))
	for _, item := range items {
		entry, ok := item.(map[string]interface{})
		if !ok {
			return nil, errors.New("each participation limit must be an object")
		}
		key, _ := entry["key"].(string)
		maximum, ok := entry["max"].(float64)
		if !ok || maximum != float64(int(maximum)) {
			return nil, errors.New("participation limit max must be an integer")
		}
		limits = append(limits, WalletSettlementLimit{Key: key, Max: int(maximum)})
	}
	return limits, nil
}
