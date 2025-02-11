package pogs

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/cloudflare/cloudflared/h2mux"
	"github.com/cloudflare/cloudflared/originservice"
	"github.com/cloudflare/cloudflared/tlsconfig"
	"github.com/cloudflare/cloudflared/tunnelrpc"

	"github.com/pkg/errors"
	capnp "zombiezen.com/go/capnproto2"
	"zombiezen.com/go/capnproto2/pogs"
	"zombiezen.com/go/capnproto2/rpc"
	"zombiezen.com/go/capnproto2/server"
)

///
/// Structs
///

// ClientConfig is a collection of FallibleConfig that determines how cloudflared should function
type ClientConfig struct {
	Version              Version               `json:"version"`
	SupervisorConfig     *SupervisorConfig     `json:"supervisor_config"`
	EdgeConnectionConfig *EdgeConnectionConfig `json:"edge_connection_config"`
	DoHProxyConfigs      []*DoHProxyConfig     `json:"doh_proxy_configs" capnp:"dohProxyConfigs"`
	ReverseProxyConfigs  []*ReverseProxyConfig `json:"reverse_proxy_configs"`
}

// Version type models the version of a ClientConfig
type Version uint64

func InitVersion() Version {
	return Version(0)
}

func (v Version) IsNewerOrEqual(comparedVersion Version) bool {
	return v >= comparedVersion
}

func (v Version) String() string {
	return fmt.Sprintf("Version: %d", v)
}

// FallibleConfig is an interface implemented by configs that cloudflared might not be able to apply
type FallibleConfig interface {
	FailReason(err error) string
	jsonType() string
}

// SupervisorConfig specifies config of components managed by Supervisor other than ConnectionManager
type SupervisorConfig struct {
	AutoUpdateFrequency    time.Duration `json:"auto_update_frequency"`
	MetricsUpdateFrequency time.Duration `json:"metrics_update_frequency"`
	GracePeriod            time.Duration `json:"grace_period"`
}

// FailReason impelents FallibleConfig interface for SupervisorConfig
func (sc *SupervisorConfig) FailReason(err error) string {
	return fmt.Sprintf("Cannot apply SupervisorConfig, err: %v", err)
}

func (sc *SupervisorConfig) MarshalJSON() ([]byte, error) {
	marshaler := make(map[string]SupervisorConfig, 1)
	marshaler[sc.jsonType()] = *sc
	return json.Marshal(marshaler)
}

func (sc *SupervisorConfig) jsonType() string {
	return "supervisor_config"
}

// EdgeConnectionConfig specifies what parameters and how may connections should ConnectionManager establish with edge
type EdgeConnectionConfig struct {
	NumHAConnections    uint8         `json:"num_ha_connections"`
	HeartbeatInterval   time.Duration `json:"heartbeat_interval"`
	Timeout             time.Duration `json:"timeout"`
	MaxFailedHeartbeats uint64        `json:"max_failed_heartbeats"`
	UserCredentialPath  string        `json:"user_credential_path"`
}

// FailReason impelents FallibleConfig interface for EdgeConnectionConfig
func (cmc *EdgeConnectionConfig) FailReason(err error) string {
	return fmt.Sprintf("Cannot apply EdgeConnectionConfig, err: %v", err)
}

func (cmc *EdgeConnectionConfig) MarshalJSON() ([]byte, error) {
	marshaler := make(map[string]EdgeConnectionConfig, 1)
	marshaler[cmc.jsonType()] = *cmc
	return json.Marshal(marshaler)
}

func (cmc *EdgeConnectionConfig) jsonType() string {
	return "edge_connection_config"
}

// DoHProxyConfig is configuration for DNS over HTTPS service
type DoHProxyConfig struct {
	ListenHost string   `json:"listen_host"`
	ListenPort uint16   `json:"listen_port"`
	Upstreams  []string `json:"upstreams"`
}

// FailReason impelents FallibleConfig interface for DoHProxyConfig
func (dpc *DoHProxyConfig) FailReason(err error) string {
	return fmt.Sprintf("Cannot apply DoHProxyConfig, err: %v", err)
}

func (dpc *DoHProxyConfig) MarshalJSON() ([]byte, error) {
	marshaler := make(map[string]DoHProxyConfig, 1)
	marshaler[dpc.jsonType()] = *dpc
	return json.Marshal(marshaler)
}

func (dpc *DoHProxyConfig) jsonType() string {
	return "doh_proxy_config"
}

// ReverseProxyConfig how and for what hostnames can this cloudflared proxy
type ReverseProxyConfig struct {
	TunnelHostname          h2mux.TunnelHostname     `json:"tunnel_hostname"`
	OriginConfigJSONHandler *OriginConfigJSONHandler `json:"origin_config"`
	Retries                 uint64                   `json:"retries"`
	ConnectionTimeout       time.Duration            `json:"connection_timeout"`
	CompressionQuality      uint64                   `json:"compression_quality"`
}

func NewReverseProxyConfig(
	tunnelHostname string,
	originConfig OriginConfig,
	retries uint64,
	connectionTimeout time.Duration,
	compressionQuality uint64,
) (*ReverseProxyConfig, error) {
	if originConfig == nil {
		return nil, fmt.Errorf("NewReverseProxyConfig: originConfigUnmarshaler was null")
	}
	return &ReverseProxyConfig{
		TunnelHostname:          h2mux.TunnelHostname(tunnelHostname),
		OriginConfigJSONHandler: &OriginConfigJSONHandler{originConfig},
		Retries:                 retries,
		ConnectionTimeout:       connectionTimeout,
		CompressionQuality:      compressionQuality,
	}, nil
}

// FailReason impelents FallibleConfig interface for ReverseProxyConfig
func (rpc *ReverseProxyConfig) FailReason(err error) string {
	return fmt.Sprintf("Cannot apply ReverseProxyConfig, err: %v", err)
}

func (rpc *ReverseProxyConfig) MarshalJSON() ([]byte, error) {
	marshaler := make(map[string]ReverseProxyConfig, 1)
	marshaler[rpc.jsonType()] = *rpc
	return json.Marshal(marshaler)
}

func (rpc *ReverseProxyConfig) jsonType() string {
	return "reverse_proxy_config"
}

//go-sumtype:decl OriginConfig
type OriginConfig interface {
	// Service returns a OriginService used to proxy to the origin
	Service() (originservice.OriginService, error)
	// go-sumtype requires at least one unexported method, otherwise it will complain that interface is not sealed
	jsonType() string
}

type originType int

const (
	httpType originType = iota
	wsType
	helloWorldType
)

func (ot originType) String() string {
	switch ot {
	case httpType:
		return "Http"
	case wsType:
		return "WebSocket"
	case helloWorldType:
		return "HelloWorld"
	default:
		return "unknown"
	}
}

type HTTPOriginConfig struct {
	URLString              string        `capnp:"urlString" json:"url_string" mapstructure:"url_string"`
	TCPKeepAlive           time.Duration `capnp:"tcpKeepAlive" json:"tcp_keep_alive" mapstructure:"tcp_keep_alive"`
	DialDualStack          bool          `json:"dial_dual_stack" mapstructure:"dial_dual_stack"`
	TLSHandshakeTimeout    time.Duration `capnp:"tlsHandshakeTimeout" json:"tls_handshake_timeout" mapstructure:"tls_handshake_timeout"`
	TLSVerify              bool          `capnp:"tlsVerify" json:"tls_verify" mapstructure:"tls_verify"`
	OriginCAPool           string        `json:"origin_ca_pool" mapstructure:"origin_ca_pool"`
	OriginServerName       string        `json:"origin_server_name" mapstructure:"origin_server_name"`
	MaxIdleConnections     uint64        `json:"max_idle_connections" mapstructure:"max_idle_connections"`
	IdleConnectionTimeout  time.Duration `json:"idle_connection_timeout" mapstructure:"idle_connection_timeout"`
	ProxyConnectionTimeout time.Duration `json:"proxy_connection_timeout" mapstructure:"proxy_connection_timeout"`
	ExpectContinueTimeout  time.Duration `json:"expect_continue_timeout" mapstructure:"expect_continue_timeout"`
	ChunkedEncoding        bool          `json:"chunked_encoding" mapstructure:"chunked_encoding"`
}

func (hc *HTTPOriginConfig) Service() (originservice.OriginService, error) {
	rootCAs, err := tlsconfig.LoadCustomOriginCA(hc.OriginCAPool)
	if err != nil {
		return nil, err
	}

	dialContext := (&net.Dialer{
		Timeout:   hc.ProxyConnectionTimeout,
		KeepAlive: hc.TCPKeepAlive,
		DualStack: hc.DialDualStack,
	}).DialContext
	transport := &http.Transport{
		Proxy:       http.ProxyFromEnvironment,
		DialContext: dialContext,
		TLSClientConfig: &tls.Config{
			RootCAs:            rootCAs,
			ServerName:         hc.OriginServerName,
			InsecureSkipVerify: hc.TLSVerify,
		},
		TLSHandshakeTimeout:   hc.TLSHandshakeTimeout,
		MaxIdleConns:          int(hc.MaxIdleConnections),
		IdleConnTimeout:       hc.IdleConnectionTimeout,
		ExpectContinueTimeout: hc.ExpectContinueTimeout,
	}
	url, err := url.Parse(hc.URLString)
	if err != nil {
		return nil, errors.Wrapf(err, "%s is not a valid URL", hc.URLString)
	}
	if url.Scheme == "unix" {
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialContext(ctx, "unix", url.Host)
		}
	}
	return originservice.NewHTTPService(transport, url, hc.ChunkedEncoding), nil
}

func (_ *HTTPOriginConfig) jsonType() string {
	return httpType.String()
}

type WebSocketOriginConfig struct {
	URLString        string `capnp:"urlString" json:"url_string" mapstructure:"url_string"`
	TLSVerify        bool   `capnp:"tlsVerify" json:"tls_verify" mapstructure:"tls_verify"`
	OriginCAPool     string `json:"origin_ca_pool" mapstructure:"origin_ca_pool"`
	OriginServerName string `json:"origin_server_name" mapstructure:"origin_server_name"`
}

func (wsc *WebSocketOriginConfig) Service() (originservice.OriginService, error) {
	rootCAs, err := tlsconfig.LoadCustomOriginCA(wsc.OriginCAPool)
	if err != nil {
		return nil, err
	}
	tlsConfig := &tls.Config{
		RootCAs:            rootCAs,
		ServerName:         wsc.OriginServerName,
		InsecureSkipVerify: wsc.TLSVerify,
	}

	url, err := url.Parse(wsc.URLString)
	if err != nil {
		return nil, errors.Wrapf(err, "%s is not a valid URL", wsc.URLString)
	}
	return originservice.NewWebSocketService(tlsConfig, url)
}

func (_ *WebSocketOriginConfig) jsonType() string {
	return wsType.String()
}

type HelloWorldOriginConfig struct{}

func (_ *HelloWorldOriginConfig) Service() (originservice.OriginService, error) {
	return nil, fmt.Errorf("not implemented error")
}

func (_ *HelloWorldOriginConfig) jsonType() string {
	return helloWorldType.String()
}

/*
 * Boilerplate to convert between these structs and the primitive structs
 * generated by capnp-go.
 * Mnemonics for variable names in this section:
 *   - `p` is for POGS (plain old Go struct)
 *   - `s` (and `ss`) is for "capnp.Struct", which is the fundamental type
 *     underlying the capnp-go data structures.
 */

func MarshalClientConfig(s tunnelrpc.ClientConfig, p *ClientConfig) error {
	s.SetVersion(uint64(p.Version))

	supervisorConfig, err := s.NewSupervisorConfig()
	if err != nil {
		return errors.Wrap(err, "failed to get SupervisorConfig")
	}
	if err = MarshalSupervisorConfig(supervisorConfig, p.SupervisorConfig); err != nil {
		return errors.Wrap(err, "MarshalSupervisorConfig error")
	}

	edgeConnectionConfig, err := s.NewEdgeConnectionConfig()
	if err != nil {
		return errors.Wrap(err, "failed to get EdgeConnectionConfig")
	}
	if err := MarshalEdgeConnectionConfig(edgeConnectionConfig, p.EdgeConnectionConfig); err != nil {
		return errors.Wrap(err, "MarshalEdgeConnectionConfig error")
	}

	if err := marshalDoHProxyConfigs(s, p.DoHProxyConfigs); err != nil {
		return errors.Wrap(err, "marshalDoHProxyConfigs error")
	}
	if err := marshalReverseProxyConfigs(s, p.ReverseProxyConfigs); err != nil {
		return errors.Wrap(err, "marshalReverseProxyConfigs error")
	}
	return nil
}

func MarshalSupervisorConfig(s tunnelrpc.SupervisorConfig, p *SupervisorConfig) error {
	if err := pogs.Insert(tunnelrpc.SupervisorConfig_TypeID, s.Struct, p); err != nil {
		return errors.Wrap(err, "failed to insert SupervisorConfig")
	}
	return nil
}

func MarshalEdgeConnectionConfig(s tunnelrpc.EdgeConnectionConfig, p *EdgeConnectionConfig) error {
	if err := pogs.Insert(tunnelrpc.EdgeConnectionConfig_TypeID, s.Struct, p); err != nil {
		return errors.Wrap(err, "failed to insert EdgeConnectionConfig")
	}
	return nil
}

func marshalDoHProxyConfigs(s tunnelrpc.ClientConfig, dohProxyConfigs []*DoHProxyConfig) error {
	capnpList, err := s.NewDohProxyConfigs(int32(len(dohProxyConfigs)))
	if err != nil {
		return err
	}
	for i, unmarshalledConfig := range dohProxyConfigs {
		err := MarshalDoHProxyConfig(capnpList.At(i), unmarshalledConfig)
		if err != nil {
			return err
		}
	}
	return nil
}

func marshalReverseProxyConfigs(s tunnelrpc.ClientConfig, reverseProxyConfigs []*ReverseProxyConfig) error {
	capnpList, err := s.NewReverseProxyConfigs(int32(len(reverseProxyConfigs)))
	if err != nil {
		return err
	}
	for i, unmarshalledConfig := range reverseProxyConfigs {
		err := MarshalReverseProxyConfig(capnpList.At(i), unmarshalledConfig)
		if err != nil {
			return err
		}
	}
	return nil
}

func UnmarshalClientConfig(s tunnelrpc.ClientConfig) (*ClientConfig, error) {
	p := new(ClientConfig)
	p.Version = Version(s.Version())

	supervisorConfig, err := s.SupervisorConfig()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get SupervisorConfig")
	}
	p.SupervisorConfig, err = UnmarshalSupervisorConfig(supervisorConfig)
	if err != nil {
		return nil, errors.Wrap(err, "UnmarshalSupervisorConfig error")
	}

	edgeConnectionConfig, err := s.EdgeConnectionConfig()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get ConnectionManagerConfig")
	}
	p.EdgeConnectionConfig, err = UnmarshalEdgeConnectionConfig(edgeConnectionConfig)
	if err != nil {
		return nil, errors.Wrap(err, "UnmarshalConnectionManagerConfig error")
	}

	p.DoHProxyConfigs, err = unmarshalDoHProxyConfigs(s)
	if err != nil {
		return nil, errors.Wrap(err, "unmarshalDoHProxyConfigs error")
	}

	p.ReverseProxyConfigs, err = unmarshalReverseProxyConfigs(s)
	if err != nil {
		return nil, errors.Wrap(err, "unmarshalReverseProxyConfigs error")
	}

	return p, nil
}

func UnmarshalSupervisorConfig(s tunnelrpc.SupervisorConfig) (*SupervisorConfig, error) {
	p := new(SupervisorConfig)
	err := pogs.Extract(p, tunnelrpc.SupervisorConfig_TypeID, s.Struct)
	return p, err
}

func UnmarshalEdgeConnectionConfig(s tunnelrpc.EdgeConnectionConfig) (*EdgeConnectionConfig, error) {
	p := new(EdgeConnectionConfig)
	err := pogs.Extract(p, tunnelrpc.EdgeConnectionConfig_TypeID, s.Struct)
	return p, err
}

func unmarshalDoHProxyConfigs(s tunnelrpc.ClientConfig) ([]*DoHProxyConfig, error) {
	var result []*DoHProxyConfig
	marshalledDoHProxyConfigs, err := s.DohProxyConfigs()
	if err != nil {
		return nil, err
	}
	for i := 0; i < marshalledDoHProxyConfigs.Len(); i++ {
		ss := marshalledDoHProxyConfigs.At(i)
		dohProxyConfig, err := UnmarshalDoHProxyConfig(ss)
		if err != nil {
			return nil, err
		}
		result = append(result, dohProxyConfig)
	}
	return result, nil
}

func unmarshalReverseProxyConfigs(s tunnelrpc.ClientConfig) ([]*ReverseProxyConfig, error) {
	var result []*ReverseProxyConfig
	marshalledReverseProxyConfigs, err := s.ReverseProxyConfigs()
	if err != nil {
		return nil, err
	}
	for i := 0; i < marshalledReverseProxyConfigs.Len(); i++ {
		ss := marshalledReverseProxyConfigs.At(i)
		reverseProxyConfig, err := UnmarshalReverseProxyConfig(ss)
		if err != nil {
			return nil, err
		}
		result = append(result, reverseProxyConfig)
	}
	return result, nil
}

func MarshalUseConfigurationResult(s tunnelrpc.UseConfigurationResult, p *UseConfigurationResult) error {
	capnpList, err := s.NewFailedConfigs(int32(len(p.FailedConfigs)))
	if err != nil {
		return errors.Wrap(err, "Cannot create new FailedConfigs")
	}
	for i, unmarshalledFailedConfig := range p.FailedConfigs {
		err := MarshalFailedConfig(capnpList.At(i), unmarshalledFailedConfig)
		if err != nil {
			return errors.Wrapf(err, "Cannot MarshalFailedConfig at index %d", i)
		}
	}
	s.SetSuccess(p.Success)
	return nil
}

func UnmarshalUseConfigurationResult(s tunnelrpc.UseConfigurationResult) (*UseConfigurationResult, error) {
	p := new(UseConfigurationResult)
	var failedConfigs []*FailedConfig
	marshalledFailedConfigs, err := s.FailedConfigs()
	if err != nil {
		return nil, errors.Wrap(err, "Cannot get FailedConfigs")
	}
	for i := 0; i < marshalledFailedConfigs.Len(); i++ {
		ss := marshalledFailedConfigs.At(i)
		failedConfig, err := UnmarshalFailedConfig(ss)
		if err != nil {
			return nil, errors.Wrapf(err, "Cannot UnmarshalFailedConfig at index %d", i)
		}
		failedConfigs = append(failedConfigs, failedConfig)
	}
	p.FailedConfigs = failedConfigs
	p.Success = s.Success()
	return p, nil
}

func MarshalDoHProxyConfig(s tunnelrpc.DoHProxyConfig, p *DoHProxyConfig) error {
	return pogs.Insert(tunnelrpc.DoHProxyConfig_TypeID, s.Struct, p)
}

func UnmarshalDoHProxyConfig(s tunnelrpc.DoHProxyConfig) (*DoHProxyConfig, error) {
	p := new(DoHProxyConfig)
	err := pogs.Extract(p, tunnelrpc.DoHProxyConfig_TypeID, s.Struct)
	return p, err
}

func MarshalReverseProxyConfig(s tunnelrpc.ReverseProxyConfig, p *ReverseProxyConfig) error {
	s.SetTunnelHostname(p.TunnelHostname.String())
	switch config := p.OriginConfigJSONHandler.OriginConfig.(type) {
	case *HTTPOriginConfig:
		ss, err := s.Origin().NewHttp()
		if err != nil {
			return err
		}
		if err := MarshalHTTPOriginConfig(ss, config); err != nil {
			return err
		}
	case *WebSocketOriginConfig:
		ss, err := s.Origin().NewWebsocket()
		if err != nil {
			return err
		}
		if err := MarshalWebSocketOriginConfig(ss, config); err != nil {
			return err
		}
	case *HelloWorldOriginConfig:
		ss, err := s.Origin().NewHelloWorld()
		if err != nil {
			return err
		}
		if err := MarshalHelloWorldOriginConfig(ss, config); err != nil {
			return err
		}
	default:
		return fmt.Errorf("Unknown type for config: %T", config)
	}
	s.SetRetries(p.Retries)
	s.SetConnectionTimeout(p.ConnectionTimeout.Nanoseconds())
	s.SetCompressionQuality(p.CompressionQuality)
	return nil
}

func UnmarshalReverseProxyConfig(s tunnelrpc.ReverseProxyConfig) (*ReverseProxyConfig, error) {
	p := new(ReverseProxyConfig)
	tunnelHostname, err := s.TunnelHostname()
	if err != nil {
		return nil, err
	}
	p.TunnelHostname = h2mux.TunnelHostname(tunnelHostname)
	switch s.Origin().Which() {
	case tunnelrpc.ReverseProxyConfig_origin_Which_http:
		ss, err := s.Origin().Http()
		if err != nil {
			return nil, err
		}
		config, err := UnmarshalHTTPOriginConfig(ss)
		if err != nil {
			return nil, err
		}
		p.OriginConfigJSONHandler = &OriginConfigJSONHandler{config}
	case tunnelrpc.ReverseProxyConfig_origin_Which_websocket:
		ss, err := s.Origin().Websocket()
		if err != nil {
			return nil, err
		}
		config, err := UnmarshalWebSocketOriginConfig(ss)
		if err != nil {
			return nil, err
		}
		p.OriginConfigJSONHandler = &OriginConfigJSONHandler{config}
	case tunnelrpc.ReverseProxyConfig_origin_Which_helloWorld:
		ss, err := s.Origin().HelloWorld()
		if err != nil {
			return nil, err
		}
		config, err := UnmarshalHelloWorldOriginConfig(ss)
		if err != nil {
			return nil, err
		}
		p.OriginConfigJSONHandler = &OriginConfigJSONHandler{config}
	}
	p.Retries = s.Retries()
	p.ConnectionTimeout = time.Duration(s.ConnectionTimeout())
	p.CompressionQuality = s.CompressionQuality()
	return p, nil
}

func MarshalHTTPOriginConfig(s tunnelrpc.HTTPOriginConfig, p *HTTPOriginConfig) error {
	return pogs.Insert(tunnelrpc.HTTPOriginConfig_TypeID, s.Struct, p)
}

func UnmarshalHTTPOriginConfig(s tunnelrpc.HTTPOriginConfig) (*HTTPOriginConfig, error) {
	p := new(HTTPOriginConfig)
	err := pogs.Extract(p, tunnelrpc.HTTPOriginConfig_TypeID, s.Struct)
	return p, err
}

func MarshalWebSocketOriginConfig(s tunnelrpc.WebSocketOriginConfig, p *WebSocketOriginConfig) error {
	return pogs.Insert(tunnelrpc.WebSocketOriginConfig_TypeID, s.Struct, p)
}

func UnmarshalWebSocketOriginConfig(s tunnelrpc.WebSocketOriginConfig) (*WebSocketOriginConfig, error) {
	p := new(WebSocketOriginConfig)
	err := pogs.Extract(p, tunnelrpc.WebSocketOriginConfig_TypeID, s.Struct)
	return p, err
}

func MarshalHelloWorldOriginConfig(s tunnelrpc.HelloWorldOriginConfig, p *HelloWorldOriginConfig) error {
	return pogs.Insert(tunnelrpc.HelloWorldOriginConfig_TypeID, s.Struct, p)
}

func UnmarshalHelloWorldOriginConfig(s tunnelrpc.HelloWorldOriginConfig) (*HelloWorldOriginConfig, error) {
	p := new(HelloWorldOriginConfig)
	err := pogs.Extract(p, tunnelrpc.HelloWorldOriginConfig_TypeID, s.Struct)
	return p, err
}

type ClientService interface {
	UseConfiguration(ctx context.Context, config *ClientConfig) (*UseConfigurationResult, error)
}

type ClientService_PogsClient struct {
	Client capnp.Client
	Conn   *rpc.Conn
}

func (c *ClientService_PogsClient) Close() error {
	return c.Conn.Close()
}

func (c *ClientService_PogsClient) UseConfiguration(
	ctx context.Context,
	config *ClientConfig,
) (*UseConfigurationResult, error) {
	client := tunnelrpc.ClientService{Client: c.Client}
	promise := client.UseConfiguration(ctx, func(p tunnelrpc.ClientService_useConfiguration_Params) error {
		clientServiceConfig, err := p.NewClientServiceConfig()
		if err != nil {
			return err
		}
		return MarshalClientConfig(clientServiceConfig, config)
	})
	retval, err := promise.Result().Struct()
	if err != nil {
		return nil, err
	}
	return UnmarshalUseConfigurationResult(retval)
}

func ClientService_ServerToClient(s ClientService) tunnelrpc.ClientService {
	return tunnelrpc.ClientService_ServerToClient(ClientService_PogsImpl{s})
}

type ClientService_PogsImpl struct {
	impl ClientService
}

func (i ClientService_PogsImpl) UseConfiguration(p tunnelrpc.ClientService_useConfiguration) error {
	config, err := p.Params.ClientServiceConfig()
	if err != nil {
		return errors.Wrap(err, "Cannot get CloudflaredConfig parameter")
	}
	pogsConfig, err := UnmarshalClientConfig(config)
	if err != nil {
		return errors.Wrap(err, "Cannot unmarshal tunnelrpc.CloudflaredConfig to *CloudflaredConfig")
	}
	server.Ack(p.Options)
	userConfigResult, err := i.impl.UseConfiguration(p.Ctx, pogsConfig)
	if err != nil {
		return err
	}
	result, err := p.Results.NewResult()
	if err != nil {
		return err
	}
	return MarshalUseConfigurationResult(result, userConfigResult)
}

type UseConfigurationResult struct {
	Success       bool            `json:"success"`
	FailedConfigs []*FailedConfig `json:"failed_configs"`
}

type FailedConfig struct {
	Config FallibleConfig `json:"config"`
	Reason string         `json:"reason"`
}

func MarshalFailedConfig(s tunnelrpc.FailedConfig, p *FailedConfig) error {
	switch config := p.Config.(type) {
	case *SupervisorConfig:
		ss, err := s.Config().NewSupervisor()
		if err != nil {
			return err
		}
		err = MarshalSupervisorConfig(ss, config)
		if err != nil {
			return err
		}
	case *EdgeConnectionConfig:
		ss, err := s.Config().NewEdgeConnection()
		if err != nil {
			return err
		}
		err = MarshalEdgeConnectionConfig(ss, config)
		if err != nil {
			return err
		}
	case *DoHProxyConfig:
		ss, err := s.Config().NewDoh()
		if err != nil {
			return err
		}
		err = MarshalDoHProxyConfig(ss, config)
		if err != nil {
			return err
		}
	case *ReverseProxyConfig:
		ss, err := s.Config().NewReverseProxy()
		if err != nil {
			return err
		}
		err = MarshalReverseProxyConfig(ss, config)
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("Unknown type for Config: %T", config)
	}
	s.SetReason(p.Reason)
	return nil
}

func UnmarshalFailedConfig(s tunnelrpc.FailedConfig) (*FailedConfig, error) {
	p := new(FailedConfig)
	switch s.Config().Which() {
	case tunnelrpc.FailedConfig_config_Which_supervisor:
		ss, err := s.Config().Supervisor()
		if err != nil {
			return nil, errors.Wrap(err, "Cannot get SupervisorConfig from Config")
		}
		config, err := UnmarshalSupervisorConfig(ss)
		if err != nil {
			return nil, errors.Wrap(err, "Cannot UnmarshalSupervisorConfig")
		}
		p.Config = config
	case tunnelrpc.FailedConfig_config_Which_edgeConnection:
		ss, err := s.Config().EdgeConnection()
		if err != nil {
			return nil, errors.Wrap(err, "Cannot get ConnectionManager from Config")
		}
		config, err := UnmarshalEdgeConnectionConfig(ss)
		if err != nil {
			return nil, errors.Wrap(err, "Cannot UnmarshalConnectionManagerConfig")
		}
		p.Config = config
	case tunnelrpc.FailedConfig_config_Which_doh:
		ss, err := s.Config().Doh()
		if err != nil {
			return nil, errors.Wrap(err, "Cannot get Doh from Config")
		}
		config, err := UnmarshalDoHProxyConfig(ss)
		if err != nil {
			return nil, errors.Wrap(err, "Cannot UnmarshalDoHProxyConfig")
		}
		p.Config = config
	case tunnelrpc.FailedConfig_config_Which_reverseProxy:
		ss, err := s.Config().ReverseProxy()
		if err != nil {
			return nil, errors.Wrap(err, "Cannot get ReverseProxy from Config")
		}
		config, err := UnmarshalReverseProxyConfig(ss)
		if err != nil {
			return nil, errors.Wrap(err, "Cannot UnmarshalReverseProxyConfig")
		}
		p.Config = config
	default:
		return nil, fmt.Errorf("Unknown type for FailedConfig: %v", s.Config().Which())
	}
	reason, err := s.Reason()
	if err != nil {
		return nil, errors.Wrap(err, "Cannot get Reason")
	}
	p.Reason = reason
	return p, nil
}
