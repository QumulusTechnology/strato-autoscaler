package qumulus

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"gopkg.in/gcfg.v1"
	netutil "k8s.io/apimachinery/pkg/util/net"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider/magnum/gophercloud/openstack/identity/v3/extensions/trusts"
	"k8s.io/autoscaler/cluster-autoscaler/config"
	"k8s.io/autoscaler/cluster-autoscaler/version"
	certutil "k8s.io/client-go/util/cert"
	"k8s.io/klog/v2"
)

// These Opts types are for parsing an OpenStack cloud-config file.
// The definitions are taken from cloud-provider-openstack.

// MyDuration is the encoding.TextUnmarshaler interface for time.Duration
type MyDuration struct {
	time.Duration
}

// UnmarshalText is used to convert from text to Duration
func (d *MyDuration) UnmarshalText(text []byte) error {
	res, err := time.ParseDuration(string(text))
	if err != nil {
		return err
	}
	d.Duration = res
	return nil
}

// LoadBalancerOpts have the options to talk to Neutron LBaaSV2 or Octavia
type LoadBalancerOpts struct {
	LBVersion            string     `gcfg:"lb-version"`          // overrides autodetection. Only support v2.
	UseOctavia           bool       `gcfg:"use-octavia"`         // uses Octavia V2 service catalog endpoint
	SubnetID             string     `gcfg:"subnet-id"`           // overrides autodetection.
	FloatingNetworkID    string     `gcfg:"floating-network-id"` // If specified, will create floating ip for loadbalancer, or do not create floating ip.
	LBMethod             string     `gcfg:"lb-method"`           // default to ROUND_ROBIN.
	LBProvider           string     `gcfg:"lb-provider"`
	CreateMonitor        bool       `gcfg:"create-monitor"`
	MonitorDelay         MyDuration `gcfg:"monitor-delay"`
	MonitorTimeout       MyDuration `gcfg:"monitor-timeout"`
	MonitorMaxRetries    uint       `gcfg:"monitor-max-retries"`
	ManageSecurityGroups bool       `gcfg:"manage-security-groups"`
	NodeSecurityGroupIDs []string   // Do not specify, get it automatically when enable manage-security-groups. TODO(FengyunPan): move it into cache
}

// BlockStorageOpts is used to talk to Cinder service
type BlockStorageOpts struct {
	BSVersion             string `gcfg:"bs-version"`        // overrides autodetection. v1 or v2. Defaults to auto
	TrustDevicePath       bool   `gcfg:"trust-device-path"` // See Issue #33128
	IgnoreVolumeAZ        bool   `gcfg:"ignore-volume-az"`
	NodeVolumeAttachLimit int    `gcfg:"node-volume-attach-limit"` // override volume attach limit for Cinder. Default is : 256
}

// RouterOpts is used for Neutron routes
type RouterOpts struct {
	RouterID string `gcfg:"router-id"` // required
}

// MetadataOpts is used for configuring how to talk to metadata service or config drive
type MetadataOpts struct {
	SearchOrder    string     `gcfg:"search-order"`
	RequestTimeout MyDuration `gcfg:"request-timeout"`
}

type StratoOpts struct {
	Host     string     `gcfg:"host"`
	ApiToken string     `gcfg:"api-token"`
	Timeout  MyDuration `gcfg:"timeout"`
}

// Config is used to read and store information from the cloud configuration file
//
// Taken from kubernetes/pkg/cloudprovider/providers/openstack/openstack.go
// LoadBalancer, BlockStorage, Route, Metadata are not needed for the autoscaler,
// but are kept so that if a cloud-config file with those sections is provided
// then the parsing will not fail.
type Config struct {
	Global struct {
		AuthURL         string `gcfg:"auth-url"`
		Username        string `gcfg:"user-name"`
		UserID          string `gcfg:"user-id"`
		Password        string `gcfg:"password"`
		TenantID        string `gcfg:"tenant-id"`
		TenantName      string `gcfg:"tenant-name"`
		TrustID         string `gcfg:"trust-id"`
		DomainID        string `gcfg:"domain-id"`
		DomainName      string `gcfg:"domain-name"`
		Region          string `gcfg:"region"`
		CAFile          string `gcfg:"ca-file"`
		TLSInsecure     string `gcfg:"tls-insecure"`
		SecretName      string `gcfg:"secret-name"`
		SecretNamespace string `gcfg:"secret-namespace"`
	}
	LoadBalancer LoadBalancerOpts
	BlockStorage BlockStorageOpts
	Route        RouterOpts
	Metadata     MetadataOpts
	Strato       StratoOpts
}

type AuthOptsExt struct {
	trusts.AuthOptsExt
}

// ToTokenV3HeadersMap allows AuthOptions to satisfy the AuthOptionsBuilder
// interface in the v3 tokens package.
func (opts AuthOptsExt) ToTokenV3HeadersMap(map[string]any) (map[string]string, error) {
	return nil, nil
}

// readConfig parses an OpenStack cloud-config file from an io.Reader.
func readConfig(configReader io.Reader) (*Config, error) {
	var cfg Config
	if configReader != nil {
		if err := gcfg.ReadInto(&cfg, configReader); err != nil {
			return nil, fmt.Errorf("couldn't read cloud config: %v", err)
		}
	}
	return &cfg, nil
}

// createProviderClient creates and authenticates a gophercloud provider client.
func createProviderClient(cfg *Config, opts config.AutoscalingOptions) (*gophercloud.ProviderClient, error) {
	if opts.ClusterName == "" {
		return nil, errors.New("the cluster-name parameter must be set")
	}

	authOpts := toAuthOptsExt(*cfg)

	provider, err := openstack.NewClient(cfg.Global.AuthURL)
	if err != nil {
		return nil, fmt.Errorf("could not create openstack client: %v", err)
	}

	userAgent := gophercloud.UserAgent{}
	userAgent.Prepend(fmt.Sprintf("cluster-autoscaler/%s", version.ClusterAutoscalerVersion))
	userAgent.Prepend(fmt.Sprintf("cluster/%s", opts.ClusterName))
	provider.UserAgent = userAgent

	klog.V(5).Infof("Using user-agent %q", userAgent.Join())

	config := &tls.Config{}
	config.InsecureSkipVerify = cfg.Global.TLSInsecure == "true"
	if cfg.Global.CAFile != "" {
		roots, err := certutil.NewPool(cfg.Global.CAFile)
		if err != nil {
			return nil, err
		}
		config.RootCAs = roots
	}
	provider.HTTPClient.Transport = netutil.SetOldTransportDefaults(&http.Transport{TLSClientConfig: config})

	err = openstack.AuthenticateV3(context.Background(), provider, authOpts, gophercloud.EndpointOpts{})
	if err != nil {
		return nil, fmt.Errorf("could not authenticate client: %v", err)
	}

	return provider, nil
}

func toAuthOptsExt(cfg Config) AuthOptsExt {
	opts := gophercloud.AuthOptions{
		IdentityEndpoint: cfg.Global.AuthURL,
		Username:         cfg.Global.Username,
		UserID:           cfg.Global.UserID,
		Password:         cfg.Global.Password,
		TenantID:         cfg.Global.TenantID,
		TenantName:       cfg.Global.TenantName,
		DomainID:         cfg.Global.DomainID,
		DomainName:       cfg.Global.DomainName,

		// Persistent service, so we need to be able to renew tokens.
		AllowReauth: true,
	}

	return AuthOptsExt{
		AuthOptsExt: trusts.AuthOptsExt{
			AuthOptionsBuilder: &opts,
			TrustID:            cfg.Global.TrustID,
		},
	}
}

func createNovaClient(cfg *Config, provider *gophercloud.ProviderClient, opts config.AutoscalingOptions) (*gophercloud.ServiceClient, error) {
	novaClient, err := openstack.NewComputeV2(provider, gophercloud.EndpointOpts{Region: cfg.Global.Region})
	if err != nil {
		return nil, fmt.Errorf("could not create compute client: %v", err)
	}

	return novaClient, nil
}
