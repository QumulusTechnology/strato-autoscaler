package qumulus

import (
	"context"
	"errors"
	"fmt"

	strato "github.com/QumulusTechnology/strato-api/models"
	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servergroups"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider"
	stratocloud "k8s.io/autoscaler/cluster-autoscaler/cloudprovider/qumulus/strato"
	"k8s.io/autoscaler/cluster-autoscaler/config"
	"k8s.io/autoscaler/cluster-autoscaler/simulator/framework"
)

// NodeGroup implements cloudprovider.NodeGroup interface. NodeGroup contains
// configuration info and functions to control a set of nodes that have the
// same capacity and set of labels.
type NodeGroup struct {
	id        string
	clusterID string
	client    nodeGroupClient
	Nova      *gophercloud.ServiceClient
	nodePool  *strato.NodePool

	minSize int
	maxSize int
}

// MaxSize returns maximum size of the node group.
func (n *NodeGroup) MaxSize() int {
	return n.maxSize
}

// MinSize returns minimum size of the node group.
func (n *NodeGroup) MinSize() int {
	return n.minSize
}

// TargetSize returns the current target size of the node group. It is possible
// that the number of nodes in Kubernetes is different at the moment but should
// be equal to Size() once everything stabilizes (new nodes finish startup and
// registration or removed nodes are deleted completely). Implementation
// required.
func (n *NodeGroup) TargetSize() (int, error) {
	return n.nodePool.NodeCount, nil
}

// IncreaseSize increases the size of the node group. To delete a node you need
// to explicitly name it and use DeleteNode. This function should wait until
// node group size is updated. Implementation required.
func (n *NodeGroup) IncreaseSize(delta int) error {
	if delta <= 0 {
		return fmt.Errorf("delta must be positive, have: %d", delta)
	}

	targetSize := n.nodePool.NodeCount + delta

	if targetSize > n.MaxSize() {
		return fmt.Errorf("size increase is too large. current: %d desired: %d max: %d", n.nodePool.NodeCount, targetSize, n.MaxSize())
	}

	updatedNodePool, err := n.client.UpdateNodePool(n.clusterID, n.id, stratocloud.UpdateNodePoolRequest{
		NodeCount:                     targetSize,
		NodesToReplace:                nil,
		GracefulReplacement:           true,
		GracefulRemoval:               true,
		EnableKubeDeleteNodeOnRemove:  true,
		EnableKubeDeleteNodeOnReplace: true,
		Wait:                          true,
	})
	if err != nil {
		return err
	}

	if updatedNodePool.NodeCount != targetSize {
		return fmt.Errorf("couldn't increase size to %d (delta: %d). Current size is: %d", targetSize, delta, updatedNodePool.NodeCount)
	}

	// update internal cache
	n.nodePool.NodeCount = targetSize
	return nil
}

// AtomicIncreaseSize is not implemented.
func (n *NodeGroup) AtomicIncreaseSize(delta int) error {
	return cloudprovider.ErrNotImplemented
}

// DeleteNodes deletes nodes from this node group (and also increasing the size
// of the node group with that). Error is returned either on failure or if the
// given node doesn't belong to this node group. This function should wait
// until node group size is updated. Implementation required.
func (n *NodeGroup) DeleteNodes(nodes []*apiv1.Node) error {
	for _, node := range nodes {
		nodeId, err := stratocloud.UUID(node.Status.NodeInfo.MachineID)
		if err != nil {
			return fmt.Errorf("failed to parse node id: %w", err)
		}
		nodeGroup, err := n.getNodeGroup(context.Background(), nodeId)
		if err != nil {
			return fmt.Errorf("failed to get node group for node %s: %w", node.Name, err)
		}

		if err := n.client.DeleteNode(n.clusterID, nodeGroup.ID, nodeId); err != nil {
			return fmt.Errorf("failed to delete node %s from node group %s: %w", node.Name, nodeGroup.ID, err)
		}
	}

	return nil
}

// ForceDeleteNodes deletes nodes from the group regardless of constraints.
func (n *NodeGroup) ForceDeleteNodes(nodes []*apiv1.Node) error {
	return cloudprovider.ErrNotImplemented
}

// DecreaseTargetSize decreases the target size of the node group. This function
// doesn't permit to delete any existing node and can be used only to reduce the
// request for new nodes that have not been yet fulfilled. Delta should be negative.
// It is assumed that cloud provider will not delete the existing nodes when there
// is an option to just decrease the target. Implementation required.
func (n *NodeGroup) DecreaseTargetSize(delta int) error {
	if delta >= 0 {
		return fmt.Errorf("delta must be negative, have: %d", delta)
	}

	targetSize := n.nodePool.NodeCount + delta
	if targetSize < n.MinSize() {
		return fmt.Errorf("size decrease is too small. current: %d desired: %d min: %d", n.nodePool.NodeCount, targetSize, n.MinSize())
	}

	updatedNodePool, err := n.client.UpdateNodePool(n.clusterID, n.id, stratocloud.UpdateNodePoolRequest{
		NodeCount:                     targetSize,
		NodesToReplace:                nil,
		GracefulReplacement:           true,
		GracefulRemoval:               true,
		EnableKubeDeleteNodeOnRemove:  true,
		EnableKubeDeleteNodeOnReplace: true,
		Wait:                          true,
	})
	if err != nil {
		return err
	}

	if updatedNodePool.NodeCount != targetSize {
		return fmt.Errorf("couldn't decrease size to %d (delta: %d). Current size is: %d", targetSize, delta, updatedNodePool.NodeCount)
	}

	// update internal cache
	n.nodePool.NodeCount = targetSize
	return nil
}

// Id returns an unique identifier of the node group.
func (n *NodeGroup) Id() string {
	return n.id
}

// Debug returns a string containing all information regarding this node group.
func (n *NodeGroup) Debug() string {
	return fmt.Sprintf("cluster ID: %s (min:%d max:%d)", n.Id(), n.MinSize(), n.MaxSize())
}

// Nodes returns a list of all nodes that belong to this node group.  It is
// required that Instance objects returned by this method have Id field set.
// Other fields are optional.
func (n *NodeGroup) Nodes() ([]cloudprovider.Instance, error) {
	if n.nodePool == nil {
		return nil, errors.New("node pool instance is not created")
	}

	ctx := context.Background()
	listOpts := servers.ListOpts{
		TagsAny: fmt.Sprintf("strato:%s", n.clusterID),
	}
	oldMicroVersion := n.Nova.Microversion
	n.Nova.Microversion = "2.71"
	allServersPages, err := servers.List(n.Nova, listOpts).AllPages(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list servers: %w", err)
	}
	n.Nova.Microversion = oldMicroVersion

	allServers, err := servers.ExtractServers(allServersPages)
	if err != nil {
		return nil, fmt.Errorf("failed to extract servers: %w", err)
	}

	instances := make([]cloudprovider.Instance, 0)

	for _, server := range allServers {
		if server.ServerGroups == nil {
			continue
		}
		if len(*server.ServerGroups) == 0 {
			continue
		}
		if (*server.ServerGroups)[0] != n.id {
			continue
		}

		instance := cloudprovider.Instance{
			Id: fmt.Sprintf("openstack:///%s", server.ID),
		}
		switch server.Status {
		case "ACTIVE":
			instance.Status.State = cloudprovider.InstanceRunning
		case "BUILD":
		case "IN_PROGRESS":
			instance.Status.State = cloudprovider.InstanceCreating
		case "DELETED":
			continue
		default:
			instance.Status.ErrorInfo = &cloudprovider.InstanceErrorInfo{
				ErrorClass:   cloudprovider.OtherErrorClass,
				ErrorCode:    "no-code-qumulus",
				ErrorMessage: fmt.Sprintf("Instance is in %s state", server.Status),
			}
		}
		instances = append(instances, instance)
	}

	return instances, nil
}

// TemplateNodeInfo returns a framework.NodeInfo structure of an empty
// (as if just started) node. This will be used in scale-up simulations to
// predict what would a new node look like if a node group was expanded. The
// returned NodeInfo is expected to have a fully populated Node object, with
// all of the labels, capacity and allocatable information as well as all pods
// that are started on the node by default, using manifest (most likely only
// kube-proxy). Implementation optional.
func (n *NodeGroup) TemplateNodeInfo() (*framework.NodeInfo, error) {
	return nil, cloudprovider.ErrNotImplemented
}

// Exist checks if the node group really exists on the cloud provider side.
// Allows to tell the theoretical node group from the real one. Implementation
// required.
func (n *NodeGroup) Exist() bool {
	return n.nodePool != nil
}

// Create creates the node group on the cloud provider side.
func (ng *NodeGroup) Create() (cloudprovider.NodeGroup, error) {
	return nil, cloudprovider.ErrAlreadyExist
}

// Delete deletes the node group on the cloud provider side.
func (ng *NodeGroup) Delete() error {
	return cloudprovider.ErrNotImplemented
}

// Autoprovisioned returns if the nodegroup is autoprovisioned.
func (ng *NodeGroup) Autoprovisioned() bool {
	return false
}

// GetOptions returns NodeGroupAutoscalingOptions that should be used for this particular
// NodeGroup. Returning a nil will result in using default options.
func (n *NodeGroup) GetOptions(defaults config.NodeGroupAutoscalingOptions) (*config.NodeGroupAutoscalingOptions, error) {
	return nil, cloudprovider.ErrNotImplemented
}

func (n *NodeGroup) getNodeGroup(ctx context.Context, serverId string) (*servergroups.ServerGroup, error) {
	oldMicroVersion := n.Nova.Microversion
	n.Nova.Microversion = "2.71"
	server, err := servers.Get(ctx, n.Nova, serverId).Extract()
	if err != nil {
		return nil, fmt.Errorf("failed to get server %s: %w", serverId, err)
	}
	n.Nova.Microversion = oldMicroVersion
	if server.ServerGroups != nil {
		for _, group := range *server.ServerGroups {
			serverGroup, err := servergroups.Get(ctx, n.Nova, group).Extract()
			if err != nil {
				return nil, fmt.Errorf("failed to get node group %s, error: %v", group, err)
			}
			if serverGroup != nil {
				return serverGroup, nil
			}
		}
	}

	return nil, fmt.Errorf("server %s is not part of any server group", serverId)
}
