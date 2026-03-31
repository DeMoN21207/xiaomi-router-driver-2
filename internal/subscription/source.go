package subscription

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

const maxSubscriptionBodySize = 2 * 1024 * 1024

type Entry struct {
	Name     string         `json:"name"`
	Address  string         `json:"address,omitempty"`
	Type     string         `json:"type"`
	Outbound map[string]any `json:"-"`
}

func FetchEntries(source string) ([]Entry, error) {
	raw, err := fetchEntriesRaw(source)
	if err != nil {
		return nil, err
	}
	return ParseEntries(raw)
}

func ParseEntries(raw string) ([]Entry, error) {
	payload := decodeSubscriptionPayload(raw)
	lines := strings.Split(payload, "\n")

	entries := make([]Entry, 0, len(lines))
	seen := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		entry, err := ParseLine(line)
		if err != nil {
			continue
		}

		key := strings.TrimSpace(entry.Name)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		entries = append(entries, entry)
	}

	if len(entries) == 0 {
		return nil, errors.New("no supported locations found in subscription")
	}

	return entries, nil
}

func ParseLine(line string) (Entry, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return Entry{}, errors.New("empty line")
	}

	switch {
	case strings.HasPrefix(line, "vmess://"):
		return parseVMess(strings.TrimPrefix(line, "vmess://"))
	case strings.HasPrefix(line, "vless://"):
		return parseVLESSOrTrojan("vless", line)
	case strings.HasPrefix(line, "trojan://"):
		return parseVLESSOrTrojan("trojan", line)
	case strings.HasPrefix(line, "ss://"):
		return parseShadowsocks(line)
	default:
		return Entry{}, fmt.Errorf("unsupported subscription line: %q", line)
	}
}

func decodeSubscriptionPayload(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}

	decoded, err := decodeBase64Flexible(trimmed)
	if err != nil {
		return trimmed
	}

	candidate := strings.TrimSpace(string(decoded))
	if !strings.Contains(candidate, "://") && !strings.Contains(candidate, "\n") {
		return trimmed
	}

	return candidate
}

func parseVMess(payload string) (Entry, error) {
	decoded, err := decodeBase64Flexible(payload)
	if err != nil {
		return Entry{}, fmt.Errorf("decode vmess payload: %w", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(decoded, &raw); err != nil {
		return Entry{}, fmt.Errorf("decode vmess JSON: %w", err)
	}

	server := stringify(raw["add"])
	port := intValue(raw["port"])
	uuid := stringify(raw["id"])
	if server == "" || port <= 0 || uuid == "" {
		return Entry{}, errors.New("vmess entry is missing server, port or uuid")
	}

	outbound := map[string]any{
		"type":        "vmess",
		"server":      server,
		"server_port": port,
		"uuid":        uuid,
	}
	if security := firstNonEmpty(stringify(raw["scy"]), stringify(raw["security"]), "auto"); security != "" {
		outbound["security"] = security
	}
	if alterID := intValue(raw["aid"]); alterID > 0 {
		outbound["alter_id"] = alterID
	}

	transportType := strings.ToLower(strings.TrimSpace(stringify(raw["net"])))
	if network := underlyingNetwork(transportType); network != "" {
		outbound["network"] = network
	}
	if transport := buildVMessTransport(transportType, raw); len(transport) > 0 {
		outbound["transport"] = transport
	}
	if tls := buildVMessTLS(raw); len(tls) > 0 {
		outbound["tls"] = tls
	}

	return Entry{
		Name:     firstNonEmpty(stringify(raw["ps"]), stringify(raw["remark"]), server),
		Address:  formatAddress(server, port),
		Type:     "vmess",
		Outbound: outbound,
	}, nil
}

func parseVLESSOrTrojan(kind, rawURI string) (Entry, error) {
	parsed, err := url.Parse(rawURI)
	if err != nil {
		return Entry{}, fmt.Errorf("parse %s URI: %w", kind, err)
	}

	server := strings.TrimSpace(parsed.Hostname())
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port <= 0 {
		return Entry{}, fmt.Errorf("%s entry has invalid port", kind)
	}

	credential := strings.TrimSpace(parsed.User.Username())
	if credential == "" {
		return Entry{}, fmt.Errorf("%s entry is missing credentials", kind)
	}

	query := parsed.Query()
	outbound := map[string]any{
		"type":        kind,
		"server":      server,
		"server_port": port,
	}

	switch kind {
	case "vless":
		outbound["uuid"] = credential
		if flow := strings.TrimSpace(query.Get("flow")); flow != "" {
			outbound["flow"] = flow
		}
		if packetEncoding := strings.TrimSpace(firstNonEmpty(query.Get("packetEncoding"), query.Get("packet_encoding"))); packetEncoding != "" {
			outbound["packet_encoding"] = packetEncoding
		}
	case "trojan":
		outbound["password"] = credential
	}

	transportType := strings.ToLower(strings.TrimSpace(firstNonEmpty(query.Get("type"), query.Get("transport"))))
	if network := underlyingNetwork(transportType); network != "" {
		outbound["network"] = network
	}
	if transport := buildTransport(transportType, query); len(transport) > 0 {
		outbound["transport"] = transport
	}
	if tls := buildTLS(strings.TrimSpace(query.Get("security")), server, query, kind == "trojan"); len(tls) > 0 {
		outbound["tls"] = tls
	}

	return Entry{
		Name:     locationNameFromURI(parsed),
		Address:  formatAddress(server, port),
		Type:     kind,
		Outbound: outbound,
	}, nil
}

func parseShadowsocks(rawURI string) (Entry, error) {
	parsed, err := url.Parse(rawURI)
	if err != nil {
		return Entry{}, fmt.Errorf("parse shadowsocks URI: %w", err)
	}

	credentials, server, port, err := decodeShadowsocksParts(rawURI)
	if err != nil {
		return Entry{}, err
	}

	method, password, ok := strings.Cut(credentials, ":")
	if !ok || strings.TrimSpace(method) == "" {
		return Entry{}, errors.New("shadowsocks entry is missing method or password")
	}

	outbound := map[string]any{
		"type":        "shadowsocks",
		"server":      server,
		"server_port": port,
		"method":      strings.TrimSpace(method),
		"password":    password,
	}
	if pluginSpec := strings.TrimSpace(parsed.Query().Get("plugin")); pluginSpec != "" {
		plugin, opts := splitPluginSpec(pluginSpec)
		if plugin != "" {
			outbound["plugin"] = plugin
		}
		if opts != "" {
			outbound["plugin_opts"] = opts
		}
	}

	return Entry{
		Name:     locationNameFromURI(parsed),
		Address:  formatAddress(server, port),
		Type:     "shadowsocks",
		Outbound: outbound,
	}, nil
}

func decodeShadowsocksParts(rawURI string) (string, string, int, error) {
	trimmed := strings.TrimPrefix(rawURI, "ss://")
	trimmed = strings.SplitN(trimmed, "#", 2)[0]
	trimmed = strings.SplitN(trimmed, "?", 2)[0]

	if strings.Contains(trimmed, "@") {
		left, right, _ := strings.Cut(trimmed, "@")
		if decodedLeft, err := decodeBase64Flexible(left); err == nil && strings.Contains(string(decodedLeft), ":") {
			left = string(decodedLeft)
		}
		server, port, err := splitHostPort(right)
		if err != nil {
			return "", "", 0, err
		}
		return left, server, port, nil
	}

	decoded, err := decodeBase64Flexible(trimmed)
	if err != nil {
		return "", "", 0, fmt.Errorf("decode shadowsocks payload: %w", err)
	}

	left, right, ok := strings.Cut(string(decoded), "@")
	if !ok {
		return "", "", 0, errors.New("invalid shadowsocks payload")
	}
	server, port, err := splitHostPort(right)
	if err != nil {
		return "", "", 0, err
	}
	return left, server, port, nil
}

func buildVMessTransport(transportType string, raw map[string]any) map[string]any {
	switch transportType {
	case "", "tcp":
		return nil
	case "ws":
		transport := map[string]any{"type": "ws"}
		if path := normalizeTransportPath(stringify(raw["path"])); path != "" {
			transport["path"] = path
		}
		if host := strings.TrimSpace(stringify(raw["host"])); host != "" {
			transport["headers"] = map[string]any{"Host": host}
		}
		return transport
	case "grpc":
		transport := map[string]any{"type": "grpc"}
		if serviceName := strings.TrimSpace(firstNonEmpty(stringify(raw["serviceName"]), stringify(raw["path"]))); serviceName != "" {
			transport["service_name"] = serviceName
		}
		return transport
	case "http":
		transport := map[string]any{"type": "http"}
		if path := normalizeTransportPath(stringify(raw["path"])); path != "" {
			transport["path"] = path
		}
		if hosts := splitCSV(stringify(raw["host"])); len(hosts) > 0 {
			transport["host"] = hosts
		}
		return transport
	case "quic":
		return map[string]any{"type": "quic"}
	case "httpupgrade", "http-upgrade":
		transport := map[string]any{"type": "httpupgrade"}
		if path := normalizeTransportPath(stringify(raw["path"])); path != "" {
			transport["path"] = path
		}
		if host := strings.TrimSpace(stringify(raw["host"])); host != "" {
			transport["host"] = host
		}
		return transport
	default:
		return nil
	}
}

func buildVMessTLS(raw map[string]any) map[string]any {
	mode := strings.ToLower(strings.TrimSpace(stringify(raw["tls"])))
	if mode == "" || mode == "none" {
		return nil
	}

	query := make(url.Values)
	copyQueryValue(query, "sni", stringify(raw["sni"]))
	copyQueryValue(query, "host", stringify(raw["host"]))
	copyQueryValue(query, "alpn", stringify(raw["alpn"]))
	copyQueryValue(query, "fp", stringify(raw["fp"]))
	copyQueryValue(query, "pbk", stringify(raw["pbk"]))
	copyQueryValue(query, "sid", stringify(raw["sid"]))
	copyQueryValue(query, "spx", stringify(raw["spx"]))
	if boolValue(raw["allowInsecure"]) {
		query.Set("allowInsecure", "1")
	}

	return buildTLS(mode, stringify(raw["add"]), query, false)
}

func buildTransport(transportType string, query url.Values) map[string]any {
	switch strings.ToLower(strings.TrimSpace(transportType)) {
	case "", "tcp":
		return nil
	case "ws", "websocket":
		transport := map[string]any{"type": "ws"}
		if path := normalizeTransportPath(query.Get("path")); path != "" {
			transport["path"] = path
		}
		if host := strings.TrimSpace(firstNonEmpty(query.Get("host"), query.Get("Host"))); host != "" {
			transport["headers"] = map[string]any{"Host": host}
		}
		return transport
	case "grpc":
		transport := map[string]any{"type": "grpc"}
		if serviceName := strings.TrimSpace(firstNonEmpty(query.Get("serviceName"), query.Get("service_name"))); serviceName != "" {
			transport["service_name"] = serviceName
		}
		return transport
	case "http":
		transport := map[string]any{"type": "http"}
		if path := normalizeTransportPath(query.Get("path")); path != "" {
			transport["path"] = path
		}
		if hosts := splitCSV(firstNonEmpty(query.Get("host"), query.Get("Host"))); len(hosts) > 0 {
			transport["host"] = hosts
		}
		return transport
	case "quic":
		return map[string]any{"type": "quic"}
	case "httpupgrade", "http-upgrade":
		transport := map[string]any{"type": "httpupgrade"}
		if path := normalizeTransportPath(query.Get("path")); path != "" {
			transport["path"] = path
		}
		if host := strings.TrimSpace(firstNonEmpty(query.Get("host"), query.Get("Host"))); host != "" {
			transport["host"] = host
		}
		return transport
	default:
		return nil
	}
}

func buildTLS(mode string, server string, query url.Values, defaultEnabled bool) map[string]any {
	mode = strings.ToLower(strings.TrimSpace(mode))
	enabled := defaultEnabled

	switch mode {
	case "", "none":
		if !defaultEnabled {
			return nil
		}
	case "tls", "reality":
		enabled = true
	default:
		if !defaultEnabled {
			return nil
		}
	}

	tls := map[string]any{"enabled": enabled}
	if serverName := firstNonEmpty(query.Get("sni"), query.Get("peer"), server); serverName != "" {
		tls["server_name"] = serverName
	}
	if boolQuery(query, "allowInsecure", "insecure") {
		tls["insecure"] = true
	}
	if alpn := splitCSV(query.Get("alpn")); len(alpn) > 0 {
		tls["alpn"] = alpn
	}
	if fingerprint := strings.TrimSpace(query.Get("fp")); fingerprint != "" {
		tls["utls"] = map[string]any{
			"enabled":     true,
			"fingerprint": fingerprint,
		}
	}
	if mode == "reality" {
		reality := map[string]any{"enabled": true}
		if publicKey := strings.TrimSpace(firstNonEmpty(query.Get("pbk"), query.Get("publicKey"))); publicKey != "" {
			reality["public_key"] = publicKey
		}
		if shortID := strings.TrimSpace(firstNonEmpty(query.Get("sid"), query.Get("shortId"), query.Get("short_id"))); shortID != "" {
			reality["short_id"] = shortID
		}
		tls["reality"] = reality
	}

	return tls
}

func decodeBase64Flexible(input string) ([]byte, error) {
	normalized := strings.TrimSpace(input)
	normalized = strings.NewReplacer("-", "+", "_", "/").Replace(normalized)
	if normalized == "" {
		return nil, errors.New("empty base64 payload")
	}
	if rem := len(normalized) % 4; rem != 0 {
		normalized += strings.Repeat("=", 4-rem)
	}
	return base64.StdEncoding.DecodeString(normalized)
}

func locationNameFromURI(parsed *url.URL) string {
	name, _ := url.PathUnescape(strings.TrimSpace(parsed.Fragment))
	if name != "" {
		return name
	}
	if host := strings.TrimSpace(parsed.Hostname()); host != "" {
		return host
	}
	return strings.TrimSpace(parsed.Host)
}

func splitHostPort(value string) (string, int, error) {
	host, portString, err := net.SplitHostPort(strings.TrimSpace(value))
	if err != nil {
		return "", 0, fmt.Errorf("invalid host:port pair: %w", err)
	}
	port, err := strconv.Atoi(strings.TrimSpace(portString))
	if err != nil || port <= 0 {
		return "", 0, errors.New("invalid port")
	}
	return host, port, nil
}

func splitPluginSpec(spec string) (string, string) {
	parts := strings.SplitN(spec, ";", 2)
	plugin := strings.TrimSpace(parts[0])
	if len(parts) == 1 {
		return plugin, ""
	}
	return plugin, strings.TrimSpace(parts[1])
}

func underlyingNetwork(transportType string) string {
	switch strings.ToLower(strings.TrimSpace(transportType)) {
	case "ws", "websocket", "grpc", "http", "httpupgrade", "http-upgrade":
		return "tcp"
	case "quic":
		return "udp"
	default:
		return ""
	}
}

func normalizeTransportPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if decoded, err := url.PathUnescape(path); err == nil && decoded != "" {
		path = decoded
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return path
}

func splitCSV(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';'
	})
	result := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		result = append(result, field)
	}
	return result
}

func boolQuery(values url.Values, keys ...string) bool {
	for _, key := range keys {
		switch strings.ToLower(strings.TrimSpace(values.Get(key))) {
		case "1", "true", "yes", "on":
			return true
		}
	}
	return false
}

func copyQueryValue(values url.Values, key string, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	values.Set(key, value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func stringify(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case float64:
		if typed == float64(int(typed)) {
			return strconv.Itoa(int(typed))
		}
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case json.Number:
		return typed.String()
	case bool:
		if typed {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}

func intValue(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case json.Number:
		parsed, _ := typed.Int64()
		return int(parsed)
	case string:
		parsed, _ := strconv.Atoi(strings.TrimSpace(typed))
		return parsed
	default:
		return 0
	}
}

func boolValue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case float64:
		return typed != 0
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "1", "true", "yes", "on":
			return true
		}
	}
	return false
}

func formatAddress(server string, port int) string {
	if strings.Contains(server, ":") {
		return "[" + server + "]:" + strconv.Itoa(port)
	}
	return server + ":" + strconv.Itoa(port)
}
