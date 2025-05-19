package qumulus

import (
	"fmt"
	"io"

	strato "github.com/QumulusTechnology/strato-api/models"
	"github.com/gophercloud/gophercloud/v2"
	stratocloud "k8s.io/autoscaler/cluster-autoscaler/cloudprovider/qumulus/strato"
	"k8s.io/autoscaler/cluster-autoscaler/config"
	"k8s.io/klog/v2"
)

type nodeGroupClient interface {
	// ListNodePools lists all the node pools found in a Kubernetes cluster.
	ListNodePools(clusterID string) ([]*strato.NodePool, error)
	// UpdateNodePool updates the details of an existing node pool.
	UpdateNodePool(clusterID, poolID string, req stratocloud.UpdateNodePoolRequest) (*strato.NodePool, error)
	// DeleteNode deletes a specific node in a node pool.
	DeleteNode(clusterID, poolID, nodeID string) error
}

type Manager struct {
	Nova      *gophercloud.ServiceClient
	client    nodeGroupClient
	clusterID string

	nodeGroupClient nodeGroupClient
	nodeGroups      []*NodeGroup
}

func newManager(configReader io.Reader, opts config.AutoscalingOptions) (*Manager, error) {
	cfg, err := readConfig(configReader)
	if err != nil {
		return nil, err
	}

	provider, err := createProviderClient(cfg, opts)
	if err != nil {
		return nil, fmt.Errorf("could not create provider client: %v", err)
	}

	novaClient, err := createNovaClient(cfg, provider, opts)
	if err != nil {
		return nil, fmt.Errorf("could not create nova client: %v", err)
	}

	client := stratocloud.NewClient(stratocloud.ClientOpts{
		Host:       cfg.Strato.Host,
		ApiToken:   cfg.Strato.ApiToken,
		Timeout:    cfg.Strato.Timeout.Duration,
		TenantName: cfg.Global.TenantName,
		TenantID:   cfg.Global.TenantID,
		Username:   cfg.Global.Username,
		Password:   cfg.Global.Password,
	})

	return &Manager{
		Nova:      novaClient,
		client:    client,
		clusterID: opts.ClusterName,
	}, nil
}

func (m *Manager) Refresh() error {
	pools, err := m.client.ListNodePools(m.clusterID)
	if err != nil {
		return err
	}

	var groups []*NodeGroup
	for _, pool := range pools {
		if pool.MaxNodeCount == 0 {
			continue
		}

		klog.V(4).Infof("adding node pool: %q name: %s min: %d max: %d", pool.Id, pool.Name, pool.MinNodeCount, pool.MaxNodeCount)

		groups = append(groups, &NodeGroup{
			id:        pool.Id,
			clusterID: m.clusterID,
			client:    m.nodeGroupClient,
			nodePool:  pool,
			minSize:   pool.MinNodeCount,
			maxSize:   pool.MaxNodeCount,
		})
	}

	if len(groups) == 0 {
		klog.V(4).Info("cluster-autoscaler is disabled. no node pools are configured")
	}

	m.nodeGroups = groups
	return nil
}
