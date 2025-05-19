package qumulus

import (
	"io"
	"os"
	"strings"

	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider"
	"k8s.io/autoscaler/cluster-autoscaler/config"
	"k8s.io/autoscaler/cluster-autoscaler/utils/errors"
	"k8s.io/autoscaler/cluster-autoscaler/utils/gpu"
	"k8s.io/klog/v2"
)

var _ cloudprovider.CloudProvider = (*qumulusCloudProvider)(nil)

const (
	// GPULabel is the label added to nodes with GPU resource.
	GPULabel = "qumulus.io/gpu-node"

	qumulusProviderIDPrefix = "openstack:///"
)

type qumulusCloudProvider struct {
	manager         *Manager
	resourceLimiter *cloudprovider.ResourceLimiter
}

func newQumulusCloudProvider(manager *Manager, rl *cloudprovider.ResourceLimiter) (*qumulusCloudProvider, error) {
	if err := manager.Refresh(); err != nil {
		return nil, err
	}

	return &qumulusCloudProvider{
		manager:         manager,
		resourceLimiter: rl,
	}, nil
}

// Name returns name of the cloud provider.
func (d *qumulusCloudProvider) Name() string {
	return cloudprovider.QumulusProviderName
}

// NodeGroups returns all node groups configured for this cloud provider.
func (d *qumulusCloudProvider) NodeGroups() []cloudprovider.NodeGroup {
	nodeGroups := make([]cloudprovider.NodeGroup, len(d.manager.nodeGroups))
	for i, ng := range d.manager.nodeGroups {
		nodeGroups[i] = ng
	}
	return nodeGroups
}

// NodeGroupForNode returns the node group for the given node, nil if the node
// should not be processed by cluster autoscaler, or non-nil error if such
// occurred. Must be implemented.
func (d *qumulusCloudProvider) NodeGroupForNode(node *apiv1.Node) (cloudprovider.NodeGroup, error) {
	providerID := node.Spec.ProviderID
	nodeID := toNodeID(providerID)

	klog.V(5).Infof("checking nodegroup for node ID: %q", nodeID)

	// NOTE(arslan): the number of node groups per cluster is usually very
	// small. So even though this looks like quadratic runtime, it's OK to
	// proceed with this.
	for _, group := range d.manager.nodeGroups {
		klog.V(5).Infof("iterating over node group %q", group.Id())
		nodes, err := group.Nodes()
		if err != nil {
			return nil, err
		}

		for _, node := range nodes {
			klog.V(6).Infof("checking node has: %q want: %q", node.Id, providerID)
			// CA uses node.Spec.ProviderID when looking for (un)registered nodes,
			// so we need to use it here too.
			if node.Id != providerID {
				continue
			}

			return group, nil
		}
	}

	// there is no "ErrNotExist" error, so we have to return a nil error
	return nil, nil
}

// HasInstance returns whether a given node has a corresponding instance in this cloud provider
func (d *qumulusCloudProvider) HasInstance(node *apiv1.Node) (bool, error) {
	return true, cloudprovider.ErrNotImplemented
}

// Pricing returns pricing model for this cloud provider or error if not
// available. Implementation optional.
func (d *qumulusCloudProvider) Pricing() (cloudprovider.PricingModel, errors.AutoscalerError) {
	return nil, cloudprovider.ErrNotImplemented
}

// GetAvailableMachineTypes get all machine types that can be requested from
// the cloud provider. Implementation optional.
func (d *qumulusCloudProvider) GetAvailableMachineTypes() ([]string, error) {
	return []string{}, nil
}

// NewNodeGroup is not implemented.
func (mcp *qumulusCloudProvider) NewNodeGroup(machineType string, labels map[string]string, systemLabels map[string]string,
	taints []apiv1.Taint, extraResources map[string]resource.Quantity) (cloudprovider.NodeGroup, error) {
	return nil, cloudprovider.ErrNotImplemented
}

// GetResourceLimiter returns resource constraints for the cloud provider
func (mcp *qumulusCloudProvider) GetResourceLimiter() (*cloudprovider.ResourceLimiter, error) {
	return mcp.resourceLimiter, nil
}

// GPULabel returns the label added to nodes with GPU resource.
func (d *qumulusCloudProvider) GPULabel() string {
	return GPULabel
}

// GetAvailableGPUTypes return all available GPU types cloud provider supports.
func (d *qumulusCloudProvider) GetAvailableGPUTypes() map[string]struct{} {
	return nil
}

// GetNodeGpuConfig returns the label, type and resource name for the GPU added to node. If node doesn't have
// any GPUs, it returns nil.
func (d *qumulusCloudProvider) GetNodeGpuConfig(node *apiv1.Node) *cloudprovider.GpuConfig {
	return gpu.GetNodeGPUFromCloudProvider(d, node)
}

// Cleanup cleans up open resources before the cloud provider is destroyed,
// i.e. go routines etc.
func (d *qumulusCloudProvider) Cleanup() error {
	return nil
}

// Refresh is called before every main loop and can be used to dynamically
// update cloud provider state. In particular the list of node groups returned
// by NodeGroups() can change as a result of CloudProvider.Refresh().
func (d *qumulusCloudProvider) Refresh() error {
	klog.V(4).Info("Refreshing node group cache")
	return d.manager.Refresh()
}

// BuildCivo builds the Civo cloud provider.
func BuildQumulus(
	opts config.AutoscalingOptions,
	do cloudprovider.NodeGroupDiscoveryOptions,
	rl *cloudprovider.ResourceLimiter,
) cloudprovider.CloudProvider {
	var configFile io.ReadCloser
	if opts.CloudConfig != "" {
		var err error
		configFile, err = os.Open(opts.CloudConfig)
		if err != nil {
			klog.Fatalf("Couldn't open cloud provider configuration %s: %#v", opts.CloudConfig, err)
		}
		defer func() {
			err := configFile.Close()
			if err == nil {
				klog.Fatalf("Failed to close Qumulus cloud provider cloud config file: %v", err)
			}
		}()
	}

	manager, err := newManager(configFile, opts)
	if err != nil {
		klog.Fatalf("Failed to create Qumulus manager: %v", err)
	}

	provider, err := newQumulusCloudProvider(manager, rl)
	if err != nil {
		klog.Fatalf("Failed to create Qumulus cloud provider: %v", err)
	}

	return provider
}

// toNodeID returns a node or droplet ID from the given provider ID.
func toNodeID(providerID string) string {
	return strings.TrimPrefix(providerID, qumulusProviderIDPrefix)
}
