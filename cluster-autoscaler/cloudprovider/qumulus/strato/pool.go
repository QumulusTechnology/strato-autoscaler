package stratocloud

import (
	"fmt"

	strato "github.com/QumulusTechnology/strato-api/models"
)

type UpdateNodePoolRequest struct {
	NodeCount                     int      `json:"node_count"`
	NodesToReplace                []string `json:"nodes_to_replace"`
	GracefulReplacement           bool     `json:"graceful_replacement"`
	GracefulRemoval               bool     `json:"graceful_removal"`
	EnableKubeDeleteNodeOnRemove  bool     `json:"enable_kube_delete_node_on_remove"`
	EnableKubeDeleteNodeOnReplace bool     `json:"enable_kube_delete_node_on_replace"`
	Wait                          bool     `json:"wait"`
}

func (c *Client) ListNodePools(clusterID string) ([]*strato.NodePool, error) {
	res := c.httpCli.Get("/v1/clusters/" + clusterID).Do()
	if res.IsErrorState() {
		return nil, fmt.Errorf("failed to list node pools: %s", res.String())
	}

	var cluster strato.Cluster
	if err := res.UnmarshalJson(&cluster); err != nil {
		return nil, fmt.Errorf("failed to unmarshal cluster: %s", err)
	}

	var nodePools []*strato.NodePool
	for _, pool := range cluster.NodePools {
		nodePools = append(nodePools, &pool)
	}

	return nodePools, nil
}

func (c *Client) UpdateNodePool(clusterID, poolID string, req UpdateNodePoolRequest) (*strato.NodePool, error) {
	res := c.httpCli.Put(fmt.Sprintf("/v1/clusters/%s/node_pools/%s", clusterID, poolID)).
		SetBodyJsonMarshal(&req).
		Do()
	if res.IsErrorState() {
		return nil, fmt.Errorf("failed to update node pool: %s", res.String())
	}

	pools, err := c.ListNodePools(clusterID)
	if err != nil {
		return nil, fmt.Errorf("failed to list node pools: %s", err)
	}

	for _, pool := range pools {
		if pool.Id == poolID {
			return pool, nil
		}
	}

	return nil, nil
}

func (c *Client) DeleteNode(clusterID, poolID, nodeID string) error {
	res := c.httpCli.Put(fmt.Sprintf("/v1/clusters/%s/node_pools/%s/node_workers/%s", clusterID, poolID, nodeID)).
		SetBodyJsonMarshal(map[string]any{
			"enable_kube_delete": true,
			"graceful":           true,
			"wait":               true,
		}).
		Do()
	if res.IsErrorState() {
		return fmt.Errorf("failed to delete node: %s", res.String())
	}

	return nil
}
